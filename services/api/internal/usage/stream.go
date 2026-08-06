package usage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const defaultUsageStreamBlock = 5 * time.Second

// StreamMessage 表示 Broker 交付的一条 usage.recorded 消息及其结算凭据。
type StreamMessage struct {
	Payload []byte // 原始事件 JSON，业务层解析前不在 Stream 适配器中解释字段。
	Receipt string // Redis Stream entry ID，用于 Ack 或保留待重试状态。
}

// StreamConsumer 抽象消息接收和结算，使 Consumer 不依赖 Redis/Valkey 客户端细节。
type StreamConsumer interface {
	Receive(context.Context) (StreamMessage, error)
	Ack(context.Context, string) error
	Nack(context.Context, string) error
}

// ValkeyUsageStream 使用 Redis/Valkey consumer group 消费 usage.recorded。
// 消息成功后 XACK；临时失败留在 pending list，空闲超过 claimIdle 后由任一健康消费者重新领取。
type ValkeyUsageStream struct {
	client    *redis.Client
	stream    string
	group     string
	consumer  string
	block     time.Duration
	claimIdle time.Duration
}

// NewValkeyUsageStream 创建或复用消费组，并为未显式配置的环境使用稳定默认名称。
// BUSYGROUP 表示消费组已经存在，是幂等启动的正常情况而不是错误。
func NewValkeyUsageStream(ctx context.Context, client *redis.Client, stream, group, consumer string) (*ValkeyUsageStream, error) {
	if client == nil {
		return nil, fmt.Errorf("valkey client is required")
	}
	if stream == "" {
		stream = "lingow:usage:recorded"
	}
	if group == "" {
		group = "lingow-usage"
	}
	if consumer == "" {
		consumer = "usage-worker"
	}
	queue := &ValkeyUsageStream{
		client:    client,
		stream:    stream,
		group:     group,
		consumer:  consumer,
		block:     defaultUsageStreamBlock,
		claimIdle: 30 * time.Second,
	}
	if err := client.XGroupCreateMkStream(ctx, stream, group, "0").Err(); err != nil && !isBusyGroup(err) {
		return nil, err
	}
	return queue, nil
}

// Publish 将一条 usage.recorded 放入 Stream，主要供测试和本地发布器使用。
func (q *ValkeyUsageStream) Publish(ctx context.Context, payload []byte) error {
	return q.client.XAdd(ctx, &redis.XAddArgs{
		Stream: q.stream,
		Values: map[string]any{"payload": payload},
	}).Err()
}

// Receive 优先重新领取超时 pending 消息，再阻塞读取从未交付的新消息。
// 每次只返回一条，便于 Consumer 在下一次领取前完成明确的 Ack/Nack 决策。
func (q *ValkeyUsageStream) Receive(ctx context.Context) (StreamMessage, error) {
	for {
		if err := ctx.Err(); err != nil {
			return StreamMessage{}, err
		}
		if message, ok, err := q.autoclaim(ctx); err != nil {
			return StreamMessage{}, err
		} else if ok {
			return message, nil
		}
		streams, err := q.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    q.group,
			Consumer: q.consumer,
			Streams:  []string{q.stream, ">"},
			Count:    1,
			Block:    q.block,
		}).Result()
		if errors.Is(err, redis.Nil) {
			if ctx.Err() != nil {
				return StreamMessage{}, ctx.Err()
			}
			continue
		}
		if err != nil {
			return StreamMessage{}, err
		}
		message, ok, err := streamMessageFromStreams(streams)
		if err != nil {
			return StreamMessage{}, err
		}
		if ok {
			return message, nil
		}
	}
}

func (q *ValkeyUsageStream) Ack(ctx context.Context, receipt string) error {
	if receipt == "" {
		return nil
	}
	return q.client.XAck(ctx, q.stream, q.group, receipt).Err()
}

func (q *ValkeyUsageStream) Nack(ctx context.Context, receipt string) error {
	if receipt == "" {
		return nil
	}
	// Redis Stream 没有独立 NACK 命令；不执行 XACK 即可把消息留在 pending list，等待 autoclaim 重试。
	return nil
}

// autoclaim 领取超过 claimIdle 仍未确认的消息，用于消费者崩溃或临时错误后的恢复。
func (q *ValkeyUsageStream) autoclaim(ctx context.Context) (StreamMessage, bool, error) {
	result, _, err := q.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   q.stream,
		Group:    q.group,
		Consumer: q.consumer,
		MinIdle:  q.claimIdle,
		Start:    "0-0",
		Count:    1,
	}).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return StreamMessage{}, false, err
	}
	if len(result) == 0 {
		return StreamMessage{}, false, nil
	}
	message, err := streamMessageFromEntry(result[0])
	if err != nil {
		// 缺少 payload 的损坏消息无法通过重试修复，直接 Ack 防止阻塞消费组。
		_ = q.Ack(ctx, result[0].ID)
		return StreamMessage{}, false, nil
	}
	return message, true, nil
}

func streamMessageFromStreams(streams []redis.XStream) (StreamMessage, bool, error) {
	if len(streams) == 0 || len(streams[0].Messages) == 0 {
		return StreamMessage{}, false, nil
	}
	message, err := streamMessageFromEntry(streams[0].Messages[0])
	if err != nil {
		return StreamMessage{}, false, err
	}
	return message, true, nil
}

func streamMessageFromEntry(entry redis.XMessage) (StreamMessage, error) {
	payload := bytesField(entry.Values, "payload")
	if len(payload) == 0 {
		return StreamMessage{}, fmt.Errorf("stream entry missing payload")
	}
	return StreamMessage{Payload: payload, Receipt: entry.ID}, nil
}

func bytesField(values map[string]any, key string) []byte {
	switch value := values[key].(type) {
	case string:
		return []byte(value)
	case []byte:
		return value
	default:
		return nil
	}
}

func isBusyGroup(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BUSYGROUP")
}
