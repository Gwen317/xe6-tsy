package usage

import (
	"context"
	"errors"
	"log/slog"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
	"github.com/1024XEngineer/xe6-tsy/services/api/metrics"
)

// Consumer 从可靠消息流消费 usage.recorded，并把事件交给用量 Service 幂等落库。
// Consumer 负责消息结算策略，Service 负责业务校验；两者分离后可以独立测试重试和计费规则。
type Consumer struct {
	stream  StreamConsumer
	service Service
}

func NewConsumer(stream StreamConsumer, service Service) *Consumer {
	return &Consumer{stream: stream, service: service}
}

// Run 持续处理消息直到 Context 取消。
// 单次临时失败只记录日志并继续循环，进程关闭信号则正常退出，不把主动取消报告成系统故障。
func (c *Consumer) Run(ctx context.Context) error {
	if c == nil || c.stream == nil || c.service == nil {
		<-ctx.Done()
		return nil
	}
	for {
		processed, err := c.ProcessOnce(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			slog.Error("usage consumer iteration failed", "error", err)
			continue
		}
		if !processed && ctx.Err() != nil {
			return nil
		}
	}
}

// ProcessOnce 最多处理一条消息，并根据结果选择 Ack 或 Nack：
// 无法解析或确定性业务错误直接 Ack 丢弃，临时依赖错误 Nack 等待重试，成功落库后 Ack。
func (c *Consumer) ProcessOnce(ctx context.Context) (bool, error) {
	message, err := c.stream.Receive(ctx)
	if err != nil {
		return false, err
	}
	if len(message.Payload) == 0 {
		_ = c.stream.Ack(ctx, message.Receipt)
		return false, nil
	}

	input, err := ParseRecordInput(message.Payload)
	if err != nil {
		// 非法 JSON 或契约字段重试也不会变正确，因此确认消息并记录 rejected 指标。
		_ = c.stream.Ack(ctx, message.Receipt)
		slog.Warn("usage consumer rejected invalid payload", "error", err, "receipt", message.Receipt)
		metrics.RecordUsageRejected()
		return true, nil
	}

	if _, err := c.service.Record(ctx, input); err != nil {
		if isPermanentUsageError(err) {
			// 归属错误、幂等冲突等属于确定性拒绝，继续投递只会形成无限重试。
			_ = c.stream.Ack(ctx, message.Receipt)
			slog.Warn("usage consumer rejected event", "error", err, "idempotency_key", input.IdempotencyKey)
			metrics.RecordUsageRejected()
			return true, nil
		}
		// 数据库或网络类临时错误不确认消息，保留在 pending list 中等待重新领取。
		_ = c.stream.Nack(ctx, message.Receipt)
		return true, err
	}
	if err := c.stream.Ack(ctx, message.Receipt); err != nil {
		return true, err
	}
	metrics.RecordUsageRecorded()
	return true, nil
}

// isPermanentUsageError 区分“重试无意义的业务错误”和“可能恢复的基础设施错误”。
func isPermanentUsageError(err error) bool {
	return errors.Is(err, domain.ErrInvalidArgument) ||
		errors.Is(err, domain.ErrForbidden) ||
		errors.Is(err, domain.ErrConflict) ||
		errors.Is(err, domain.ErrNotFound)
}
