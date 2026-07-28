package delivery

import (
	"context"
	"errors"
	"time"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
)

type WorkerDependencies struct {
	Repository   Repository
	Destinations DestinationReader
	Provider     Provider
}

type Worker struct {
	queue Queue
	deps  WorkerDependencies
}

func NewWorker(queue Queue, dependencies ...WorkerDependencies) *Worker {
	worker := &Worker{queue: queue}
	if len(dependencies) > 0 {
		worker.deps = dependencies[0]
	}
	return worker
}

func (w *Worker) Run(ctx context.Context) error {
	if w.queue == nil || w.deps.Repository == nil || w.deps.Destinations == nil || w.deps.Provider == nil {
		<-ctx.Done()
		return nil
	}
	for {
		item, err := w.queue.Receive(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return err
		}
		if err := w.Process(ctx, item); err != nil {
			return err
		}
	}
}

func (w *Worker) Process(ctx context.Context, item QueueMessage) error {
	existing, err := w.deps.Repository.GetAttempt(ctx, item.AttemptID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return w.queue.Ack(ctx, item.Receipt)
		}
		return err
	}
	reader, ok := w.deps.Repository.(WorkerMessageReader)
	if !ok {
		return errors.New("delivery repository does not expose worker message reads")
	}
	attempt, err := w.deps.Repository.ClaimAttempt(ctx, item.AttemptID)
	if errors.Is(err, domain.ErrConflict) {
		if existing.Status != AttemptStatusSending {
			// A duplicate broker delivery lost the atomic claim race. The winner
			// owns the provider call, so this receipt can be settled.
			return w.queue.Ack(ctx, item.Receipt)
		}
		// A previous worker may have reached the provider but died before the
		// terminal transaction. Replaying is safe only when the external provider
		// applies the durable attempt ID as an idempotency key.
		if !providerSupportsIdempotency(w.deps.Provider) {
			code := deliveryUnknownErrorCode
			if completeErr := w.deps.Repository.CompleteAttempt(ctx, existing.ID, existing.MessageID, AttemptStatusFailed, MessageStatusFailed, &code); completeErr != nil {
				return completeErr
			}
			return w.queue.Ack(ctx, item.Receipt)
		}
		attempt = existing
		err = nil
	}
	if err != nil {
		return err
	}
	message, err := reader.GetMessageForWorker(ctx, attempt.MessageID)
	if err != nil {
		return err
	}
	destination, err := w.deps.Destinations.ResolveVerifiedDestination(ctx, message.AccountID, message.Channel, message.DestinationRef)
	if err == nil {
		err = w.deps.Provider.Send(ctx, SendRequest{
			Message:                message,
			Attempt:                attempt,
			Destination:            destination,
			ProviderIdempotencyKey: attempt.ID,
		})
	}
	if err != nil {
		code := "provider_error"
		if completeErr := w.deps.Repository.CompleteAttempt(ctx, attempt.ID, message.ID, AttemptStatusFailed, MessageStatusFailed, &code); completeErr != nil {
			return completeErr
		}
		return w.queue.Ack(ctx, item.Receipt)
	}
	if err := w.deps.Repository.CompleteAttempt(ctx, attempt.ID, message.ID, AttemptStatusSucceeded, MessageStatusSent, nil); err != nil {
		return err
	}
	return w.queue.Ack(ctx, item.Receipt)
}

func providerSupportsIdempotency(provider Provider) bool {
	capable, ok := provider.(IdempotentProvider)
	return ok && capable.SupportsProviderIdempotency()
}

// RetryAfterFailure is a small helper for dispatchers that choose delayed
// broker redelivery rather than exposing an immediate public retry endpoint.
func RetryAfterFailure(ctx context.Context, queue Queue, item QueueMessage, attempt int) error {
	delay := time.Duration(attempt*attempt) * time.Second
	return queue.Nack(ctx, item.Receipt, time.Now().Add(delay))
}
