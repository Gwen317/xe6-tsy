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
	// Receive 只负责从消息流拿到一条消息和它的 receipt，不在这里解释业务字段。
	// receipt 是后续 Ack/Nack 的唯一依据：Ack 表示这条消息已经结算，Nack 表示保留待重试。
	message, err := c.stream.Receive(ctx)
	if err != nil {
		return false, err
	}
	if len(message.Payload) == 0 {
		// 空 payload 没有任何可恢复的业务信息，重试不会让它变成合法事件。
		// 因此直接确认并丢弃，避免一条损坏消息永久占住消费组。
		_ = c.stream.Ack(ctx, message.Receipt)
		return false, nil
	}

	// ParseRecordInput 同时完成 JSON 解码、事件版本转换和字段校验。
	// 只有通过这一关的强类型事实才能进入账户归属和幂等落库，避免消费者用动态字段猜测计费含义。
	input, err := ParseRecordInput(message.Payload)
	if err != nil {
		// 非法 JSON 或契约字段重试也不会变正确，因此确认消息并记录 rejected 指标。
		_ = c.stream.Ack(ctx, message.Receipt)
		slog.Warn("usage consumer rejected invalid payload", "error", err, "receipt", message.Receipt)
		metrics.RecordUsageRejected()
		return true, nil
	}

	// Service.Record 负责判断“这条事实是否属于该 Session”以及“是否已经记录过”。
	// Consumer 不复制这些业务规则，只根据错误是否可恢复决定消息结算方式。
	if _, err := c.service.Record(ctx, input); err != nil {
		if isPermanentUsageError(err) {
			// 归属错误、幂等冲突等属于确定性拒绝，继续投递只会形成无限重试。
			_ = c.stream.Ack(ctx, message.Receipt)
			slog.Warn("usage consumer rejected event", "error", err, "idempotency_key", input.IdempotencyKey)
			metrics.RecordUsageRejected()
			return true, nil
		}
		// 依赖暂时不可用时不能 Ack，否则消息会在数据库恢复前永久丢失。
		// Nack 在 Redis/Valkey 适配器中表现为不执行 XACK，消息会留在 pending list 中。
		// 数据库或网络类临时错误不确认消息，保留在 pending list 中等待重新领取。
		_ = c.stream.Nack(ctx, message.Receipt)
		return true, err
	}
	// 只有业务事实成功落库后才 Ack。Ack 失败仍然返回错误，允许外层记录并在下次领取时依靠幂等键安全重放。
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
