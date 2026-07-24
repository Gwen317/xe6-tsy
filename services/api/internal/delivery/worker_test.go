package delivery

import (
	"context"
	"errors"
	"testing"
	"time"
)

type queueFake struct {
	receive func(context.Context) (QueueMessage, error)
	acked   string
	nacked  string
	nextAt  time.Time
}

func (f *queueFake) Enqueue(context.Context, string, string) error { return nil }
func (f *queueFake) Receive(ctx context.Context) (QueueMessage, error) {
	return f.receive(ctx)
}
func (f *queueFake) Ack(_ context.Context, receipt string) error {
	f.acked = receipt
	return nil
}
func (f *queueFake) Nack(_ context.Context, receipt string, nextAt time.Time) error {
	f.nacked = receipt
	f.nextAt = nextAt
	return nil
}

type providerFake struct {
	request SendRequest
	err     error
}

func (f *providerFake) Send(_ context.Context, request SendRequest) error {
	f.request = request
	return f.err
}

type providerFailureFake struct {
	code      string
	retryable bool
}

func (f providerFailureFake) Error() string   { return f.code }
func (f providerFailureFake) Code() string    { return f.code }
func (f providerFailureFake) Retryable() bool { return f.retryable }

func workerFixture(sendErr error) (*Worker, *queueFake, *deliveryRepositoryFake, *providerFake) {
	queue := &queueFake{}
	repository := &deliveryRepositoryFake{
		claimed: true,
		preferences: []Preference{{
			Channel: ChannelEmail, Enabled: true, Verified: true,
		}},
		work: AttemptWork{
			Message: Message{ID: "message-1", AccountID: "account-1", Channel: ChannelEmail, DestinationRef: "email-1"},
			Attempt: DeliveryAttempt{ID: "attempt-1", MessageID: "message-1", AttemptNumber: 1, Status: AttemptStatusSending},
		},
	}
	provider := &providerFake{err: sendErr}
	worker := NewWorker(queue, repository, destinationReaderFake{destination: VerifiedDestination{
		AccountID: "account-1", Channel: ChannelEmail, DestinationRef: "email-1", ProviderTarget: "target",
	}}, provider)
	worker.now = func() time.Time { return time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC) }
	worker.newID = func(string) (string, error) { return "attempt-2", nil }
	worker.jitter = func(time.Duration) time.Duration { return 3 * time.Second }
	return worker, queue, repository, provider
}

func TestWorkerStopsWhenContextIsCancelled(t *testing.T) {
	worker, queue, _, _ := workerFixture(nil)
	queue.receive = func(ctx context.Context) (QueueMessage, error) {
		<-ctx.Done()
		return QueueMessage{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestWorkerCompletesSuccessfulAttemptBeforeAck(t *testing.T) {
	worker, queue, repository, provider := workerFixture(nil)

	err := worker.process(context.Background(), QueueMessage{AttemptID: "attempt-1", Receipt: "receipt-1"})
	if err != nil {
		t.Fatalf("process() error = %v", err)
	}
	if repository.completion.AttemptStatus != AttemptStatusSucceeded || repository.completion.MessageStatus != MessageStatusSent {
		t.Fatalf("completion = %#v", repository.completion)
	}
	if queue.acked != "receipt-1" || queue.nacked != "" || provider.request.Attempt.ID != "attempt-1" {
		t.Fatalf("queue ack=%q nack=%q request=%#v", queue.acked, queue.nacked, provider.request)
	}
}

func TestWorkerCreatesAtomicRetryForTransientFailure(t *testing.T) {
	worker, queue, repository, _ := workerFixture(providerFailureFake{code: "email_timeout", retryable: true})

	err := worker.process(context.Background(), QueueMessage{AttemptID: "attempt-1", Receipt: "receipt-1"})
	if err != nil {
		t.Fatalf("process() error = %v", err)
	}
	completion := repository.completion
	if completion.AttemptStatus != AttemptStatusFailed || completion.MessageStatus != MessageStatusRetrying || completion.ErrorCode == nil || *completion.ErrorCode != "email_timeout" {
		t.Fatalf("completion = %#v", completion)
	}
	if completion.NextAttempt == nil || completion.NextAttempt.AttemptNumber != 2 || completion.NextAttempt.ID != "attempt-2" {
		t.Fatalf("next attempt = %#v", completion.NextAttempt)
	}
	wantNextAt := worker.now().Add(defaultRetryBaseDelay + 3*time.Second)
	if completion.NextAttempt.NextAttemptAt == nil || !completion.NextAttempt.NextAttemptAt.Equal(wantNextAt) {
		t.Fatalf("next attempt time = %v, want %v", completion.NextAttempt.NextAttemptAt, wantNextAt)
	}
	if queue.acked != "receipt-1" {
		t.Fatalf("ack = %q, want receipt-1", queue.acked)
	}
}

func TestWorkerStopsAutomaticRetryAtMaximumAttempt(t *testing.T) {
	worker, queue, repository, _ := workerFixture(providerFailureFake{code: "email_timeout", retryable: true})
	repository.work.Attempt.AttemptNumber = maxAttempts

	err := worker.process(context.Background(), QueueMessage{AttemptID: "attempt-3", Receipt: "receipt-3"})
	if err != nil {
		t.Fatalf("process() error = %v", err)
	}
	completion := repository.completion
	if completion.MessageStatus != MessageStatusFailed || completion.NextAttempt != nil {
		t.Fatalf("completion = %#v", completion)
	}
	if queue.acked != "receipt-3" {
		t.Fatalf("ack = %q, want receipt-3", queue.acked)
	}
}

func TestWorkerNacksWhenAtomicClaimFails(t *testing.T) {
	worker, queue, repository, _ := workerFixture(nil)
	repository.claimErr = errors.New("database unavailable")

	err := worker.process(context.Background(), QueueMessage{AttemptID: "attempt-1", Receipt: "receipt-1"})
	if err != nil {
		t.Fatalf("process() error = %v", err)
	}
	if queue.nacked != "receipt-1" || queue.acked != "" {
		t.Fatalf("ack=%q nack=%q", queue.acked, queue.nacked)
	}
}
