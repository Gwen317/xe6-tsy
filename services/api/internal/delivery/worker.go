package delivery

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"time"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
)

const (
	defaultRetryBaseDelay = 30 * time.Second
	defaultNackDelay      = 5 * time.Second
)

type Worker struct {
	queue          Queue
	repository     Repository
	destinations   DestinationReader
	provider       Provider
	now            func() time.Time
	newID          func(string) (string, error)
	retryBaseDelay time.Duration
	jitter         func(time.Duration) time.Duration
}

func NewWorker(queue Queue, repository Repository, destinations DestinationReader, provider Provider) *Worker {
	return &Worker{
		queue:          queue,
		repository:     repository,
		destinations:   destinations,
		provider:       provider,
		now:            time.Now,
		newID:          secureID,
		retryBaseDelay: defaultRetryBaseDelay,
		jitter:         randomJitter,
	}
}

func (w *Worker) Run(ctx context.Context) error {
	if w.queue == nil || w.repository == nil || w.destinations == nil || w.provider == nil || w.now == nil || w.newID == nil {
		return domain.ErrNotImplemented
	}
	for {
		message, err := w.queue.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if err := w.process(ctx, message); err != nil {
			return err
		}
	}
}

func (w *Worker) process(ctx context.Context, queued QueueMessage) error {
	now := w.now().UTC()
	work, claimed, err := w.repository.ClaimAttempt(ctx, queued.AttemptID, now)
	if err != nil {
		return w.nack(ctx, queued.Receipt, now, err)
	}
	if !claimed {
		return w.queue.Ack(ctx, queued.Receipt)
	}

	preferences, sendErr := w.repository.ListPreferences(ctx, work.Message.AccountID)
	if sendErr == nil && !channelEnabled(preferences, work.Message.Channel) {
		sendErr = providerFailure{"channel_disabled", false}
	}
	var destination VerifiedDestination
	if sendErr == nil {
		destination, sendErr = w.destinations.ResolveVerifiedDestination(
			ctx,
			work.Message.AccountID,
			work.Message.Channel,
			work.Message.DestinationRef,
		)
	}
	if sendErr == nil && (destination.AccountID != work.Message.AccountID || destination.Channel != work.Message.Channel || destination.DestinationRef != work.Message.DestinationRef || destination.ProviderTarget == "") {
		sendErr = providerFailure{"destination_unavailable", false}
	}
	if sendErr == nil {
		sendErr = w.provider.Send(ctx, SendRequest{
			Message:     work.Message,
			Attempt:     work.Attempt,
			Destination: destination,
		})
	}

	completion, err := w.completion(work, sendErr, now)
	if err != nil {
		return w.nack(ctx, queued.Receipt, now, err)
	}
	if err := w.repository.CompleteAttempt(ctx, completion); err != nil {
		return w.nack(ctx, queued.Receipt, now, err)
	}
	return w.queue.Ack(ctx, queued.Receipt)
}

func (w *Worker) completion(work AttemptWork, sendErr error, now time.Time) (AttemptCompletion, error) {
	if work.Message.ID == "" || work.Attempt.ID == "" || work.Attempt.MessageID != work.Message.ID || work.Attempt.AttemptNumber < 1 {
		return AttemptCompletion{}, domain.ErrInvalidArgument
	}
	completion := AttemptCompletion{
		AttemptID:  work.Attempt.ID,
		MessageID:  work.Message.ID,
		FinishedAt: now,
	}
	if sendErr == nil {
		completion.AttemptStatus = AttemptStatusSucceeded
		completion.MessageStatus = MessageStatusSent
		return completion, nil
	}

	code, retryable := classifySendError(sendErr)
	completion.AttemptStatus = AttemptStatusFailed
	completion.MessageStatus = MessageStatusFailed
	completion.ErrorCode = &code
	if !retryable || work.Attempt.AttemptNumber >= maxAttempts {
		return completion, nil
	}

	nextID, err := w.newID(attemptIDPrefix)
	if err != nil {
		return AttemptCompletion{}, err
	}
	delay := w.retryBaseDelay << (work.Attempt.AttemptNumber - 1)
	nextAt := now.Add(delay + w.jitter(delay))
	completion.MessageStatus = MessageStatusRetrying
	completion.NextAttempt = &DeliveryAttempt{
		ID:            nextID,
		MessageID:     work.Message.ID,
		AttemptNumber: work.Attempt.AttemptNumber + 1,
		Status:        AttemptStatusQueued,
		NextAttemptAt: &nextAt,
		CreatedAt:     now,
	}
	return completion, nil
}

type providerFailure struct {
	code      string
	retryable bool
}

func (f providerFailure) Error() string   { return f.code }
func (f providerFailure) Code() string    { return f.code }
func (f providerFailure) Retryable() bool { return f.retryable }

func randomJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	value, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0
	}
	return time.Duration(value.Int64())
}

func (w *Worker) nack(ctx context.Context, receipt string, now time.Time, cause error) error {
	if receipt == "" {
		return cause
	}
	if err := w.queue.Nack(ctx, receipt, now.Add(defaultNackDelay)); err != nil {
		return errors.Join(cause, err)
	}
	return nil
}

func classifySendError(err error) (string, bool) {
	var failure ProviderFailure
	if errors.As(err, &failure) && failure.Code() != "" {
		return failure.Code(), failure.Retryable()
	}
	switch {
	case errors.Is(err, domain.ErrForbidden), errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrInvalidArgument):
		return "destination_unavailable", false
	default:
		return "provider_unavailable", true
	}
}
