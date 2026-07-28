package delivery

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ValkeyQueue maps the delivery Queue port to a Redis Streams consumer group.
// Stream IDs are used as broker receipts; pending entries remain recoverable
// by a later consumer after a process crash.
type ValkeyQueue struct {
	client       *redis.Client
	stream       string
	group        string
	consumer     string
	mu           sync.Mutex
	pending      []QueueMessage
	lastRecovery time.Time
}

// A pending entry is considered abandoned only after it has been idle for a
// minute. This avoids stealing work from a live consumer while still
// recovering entries left behind by a crashed worker. Entries explicitly
// Nack'ed by this consumer are kept in a local buffer and are available
// immediately, so they do not wait for this threshold.
const pendingRecoveryMinIdle = time.Minute

func NewValkeyQueue(ctx context.Context, client *redis.Client, stream, group, consumer string) (*ValkeyQueue, error) {
	if client == nil || stream == "" || group == "" || consumer == "" {
		return nil, errors.New("invalid Valkey queue configuration")
	}
	queue := &ValkeyQueue{client: client, stream: stream, group: group, consumer: consumer}
	err := client.XGroupCreateMkStream(ctx, stream, group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return nil, err
	}
	return queue, nil
}

func (q *ValkeyQueue) Enqueue(ctx context.Context, attemptID, idempotencyKey string) error {
	_, err := q.client.XAdd(ctx, &redis.XAddArgs{Stream: q.stream, Values: map[string]any{"attempt_id": attemptID, "idempotency_key": idempotencyKey}}).Result()
	return err
}

func (q *ValkeyQueue) Receive(ctx context.Context) (QueueMessage, error) {
	for {
		if message, ok := q.takePending(); ok {
			return message, nil
		}
		if err := q.recoverPending(ctx); err != nil {
			return QueueMessage{}, err
		}
		if message, ok := q.takePending(); ok {
			return message, nil
		}

		// Entries claimed by this consumer (including a Nack'ed entry after a
		// process restart with the same consumer name) are read with ID 0. Redis
		// Streams never returns these entries for the ">" cursor.
		result, err := q.client.XReadGroup(ctx, pendingReadArgs(q)).Result()
		if errors.Is(err, redis.Nil) {
			// There is no pending entry for this consumer; continue with new work.
		} else if err != nil {
			return QueueMessage{}, err
		} else if message, ok, parseErr := q.firstMessage(ctx, result); parseErr != nil {
			return QueueMessage{}, parseErr
		} else if ok {
			return message, nil
		}

		result, err = q.client.XReadGroup(ctx, newEntryReadArgs(q)).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return QueueMessage{}, err
		}
		if message, ok, parseErr := q.firstMessage(ctx, result); parseErr != nil {
			return QueueMessage{}, parseErr
		} else if ok {
			return message, nil
		}
	}
}

func pendingReadArgs(q *ValkeyQueue) *redis.XReadGroupArgs {
	// go-redis omits BLOCK when Block is negative. A zero duration would encode
	// BLOCK 0, which waits forever when this consumer has no pending entries.
	return &redis.XReadGroupArgs{Group: q.group, Consumer: q.consumer, Streams: []string{q.stream, "0"}, Count: 1, Block: -1}
}

func newEntryReadArgs(q *ValkeyQueue) *redis.XReadGroupArgs {
	return &redis.XReadGroupArgs{Group: q.group, Consumer: q.consumer, Streams: []string{q.stream, ">"}, Count: 1, Block: 5 * time.Second}
}

func (q *ValkeyQueue) Ack(ctx context.Context, receipt string) error {
	return q.client.XAck(ctx, q.stream, q.group, receipt).Err()
}

func (q *ValkeyQueue) Nack(ctx context.Context, receipt string, at time.Time) error {
	delay := time.Until(at)
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// XCLAIM transfers a pending entry back to this consumer. Redis does not
	// expose claimed entries through the ">" cursor, so retain the returned
	// message for the next Receive call. If the process dies before that call,
	// recoverPending will claim it after the idle threshold on restart.
	messages, err := q.client.XClaim(ctx, &redis.XClaimArgs{Stream: q.stream, Group: q.group, Consumer: q.consumer, MinIdle: 0, Messages: []string{receipt}}).Result()
	if err != nil {
		return err
	}
	q.appendPending(ctx, messages)
	return nil
}

func (q *ValkeyQueue) takePending() (QueueMessage, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return QueueMessage{}, false
	}
	message := q.pending[0]
	q.pending[0] = QueueMessage{}
	q.pending = q.pending[1:]
	return message, true
}

// recoverPending periodically transfers abandoned entries from other
// consumers. The interval is local-only; Redis remains the source of truth for
// ownership and pending idle time.
func (q *ValkeyQueue) recoverPending(ctx context.Context) error {
	now := time.Now()
	q.mu.Lock()
	if !q.lastRecovery.IsZero() && now.Sub(q.lastRecovery) < pendingRecoveryMinIdle {
		q.mu.Unlock()
		return nil
	}
	q.mu.Unlock()

	start := "0-0"
	for {
		messages, next, err := q.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   q.stream,
			Group:    q.group,
			Consumer: q.consumer,
			MinIdle:  pendingRecoveryMinIdle,
			Start:    start,
			Count:    50,
		}).Result()
		if err == nil {
			q.appendPending(ctx, messages)
			if next == "0-0" || next == start {
				q.markRecovery(now)
				return nil
			}
			start = next
			continue
		}
		// Valkey versions predating XAUTOCLAIM still expose XPENDING/XCLAIM.
		// Keep the recovery path usable for those deployments rather than
		// silently leaving abandoned entries stuck forever.
		if !strings.Contains(strings.ToLower(err.Error()), "unknown command") {
			return err
		}
		if err := q.recoverPendingLegacy(ctx); err != nil {
			return err
		}
		q.markRecovery(now)
		return nil
	}
}

func (q *ValkeyQueue) markRecovery(at time.Time) {
	q.mu.Lock()
	q.lastRecovery = at
	q.mu.Unlock()
}

func (q *ValkeyQueue) recoverPendingLegacy(ctx context.Context) error {
	pending, err := q.client.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: q.stream,
		Group:  q.group,
		Start:  "-",
		End:    "+",
		Count:  50,
	}).Result()
	if err != nil {
		return err
	}
	for _, entry := range pending {
		if entry.Idle < pendingRecoveryMinIdle {
			continue
		}
		messages, err := q.client.XClaim(ctx, &redis.XClaimArgs{
			Stream:   q.stream,
			Group:    q.group,
			Consumer: q.consumer,
			MinIdle:  pendingRecoveryMinIdle,
			Messages: []string{entry.ID},
		}).Result()
		if err != nil {
			return err
		}
		q.appendPending(ctx, messages)
	}
	return nil
}

func (q *ValkeyQueue) appendPending(ctx context.Context, messages []redis.XMessage) {
	for _, message := range messages {
		attemptID, ok := stringValue(message.Values["attempt_id"])
		if !ok || attemptID == "" {
			// Malformed broker entries cannot be processed safely. Ack them so a
			// poison entry does not block the consumer group forever.
			_ = q.client.XAck(ctx, q.stream, q.group, message.ID).Err()
			continue
		}
		q.mu.Lock()
		q.pending = append(q.pending, QueueMessage{AttemptID: attemptID, Receipt: message.ID})
		q.mu.Unlock()
	}
}

func (q *ValkeyQueue) firstMessage(ctx context.Context, streams []redis.XStream) (QueueMessage, bool, error) {
	for _, stream := range streams {
		for _, message := range stream.Messages {
			attemptID, ok := stringValue(message.Values["attempt_id"])
			if !ok || attemptID == "" {
				if err := q.client.XAck(ctx, q.stream, q.group, message.ID).Err(); err != nil {
					return QueueMessage{}, false, err
				}
				continue
			}
			return QueueMessage{AttemptID: attemptID, Receipt: message.ID}, true, nil
		}
	}
	return QueueMessage{}, false, nil
}

func stringValue(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	default:
		return "", false
	}
}

var _ Queue = (*ValkeyQueue)(nil)
