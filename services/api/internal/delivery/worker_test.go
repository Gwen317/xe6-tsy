package delivery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
)

type queueStub struct{}

func (queueStub) Enqueue(context.Context, string, string) error { return nil }
func (queueStub) Receive(context.Context) (QueueMessage, error) { return QueueMessage{}, nil }
func (queueStub) Ack(context.Context, string) error             { return nil }
func (queueStub) Nack(context.Context, string, time.Time) error { return nil }

func TestWorkerStopsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	worker := NewWorker(queueStub{})
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}

type workerQueueStub struct {
	acks int
}

func (*workerQueueStub) Enqueue(context.Context, string, string) error { return nil }
func (*workerQueueStub) Receive(context.Context) (QueueMessage, error) {
	return QueueMessage{}, context.Canceled
}
func (q *workerQueueStub) Ack(context.Context, string) error {
	q.acks++
	return nil
}
func (*workerQueueStub) Nack(context.Context, string, time.Time) error { return nil }

type workerRepositoryStub struct {
	retryRepositoryStub
	attempt       DeliveryAttempt
	getAttemptErr error
	claimErr      error
	completeErr   error
	completed     int
	completedCode *string
	message       Message
}

func (r *workerRepositoryStub) GetAttempt(context.Context, string) (DeliveryAttempt, error) {
	return r.attempt, r.getAttemptErr
}
func (r *workerRepositoryStub) ClaimAttempt(context.Context, string) (DeliveryAttempt, error) {
	return r.attempt, r.claimErr
}
func (r *workerRepositoryStub) GetMessageForWorker(context.Context, string) (Message, error) {
	return r.message, nil
}
func (r *workerRepositoryStub) CompleteAttempt(_ context.Context, _ string, _ string, _ DeliveryAttemptStatus, _ MessageStatus, code *string) error {
	r.completed++
	r.completedCode = code
	return r.completeErr
}

type workerDestinationStub struct{}

func (workerDestinationStub) ResolveVerifiedDestination(context.Context, string, Channel, string) (VerifiedDestination, error) {
	return VerifiedDestination{ProviderTarget: "target"}, nil
}

type workerProviderStub struct {
	calls   int
	err     error
	request SendRequest
}

func (p *workerProviderStub) Send(_ context.Context, request SendRequest) error {
	p.calls++
	p.request = request
	return p.err
}

func newWorkerRepositoryStub() *workerRepositoryStub {
	return &workerRepositoryStub{
		attempt: DeliveryAttempt{ID: "attempt-1", MessageID: "message-1", Status: AttemptStatusQueued},
		message: Message{ID: "message-1", AccountID: "account-1", Channel: ChannelEmail, DestinationRef: "destination"},
	}
}

func TestWorkerDuplicateDeliveryAcknowledgesWithoutSecondProviderCall(t *testing.T) {
	queue := &workerQueueStub{}
	repository := newWorkerRepositoryStub()
	repository.claimErr = domain.ErrConflict
	provider := &workerProviderStub{}
	worker := NewWorker(queue, WorkerDependencies{Repository: repository, Destinations: workerDestinationStub{}, Provider: provider})

	if err := worker.Process(t.Context(), QueueMessage{AttemptID: "attempt-1", Receipt: "receipt-1"}); err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if provider.calls != 0 || queue.acks != 1 {
		t.Fatalf("provider calls=%d acks=%d, want 0 and 1", provider.calls, queue.acks)
	}
}

func TestWorkerDoesNotAcknowledgeTransientAttemptReadFailure(t *testing.T) {
	queue := &workerQueueStub{}
	repository := newWorkerRepositoryStub()
	repository.getAttemptErr = errors.New("database unavailable")
	worker := NewWorker(queue, WorkerDependencies{Repository: repository, Destinations: workerDestinationStub{}, Provider: &workerProviderStub{}})

	if err := worker.Process(t.Context(), QueueMessage{AttemptID: "attempt-1", Receipt: "receipt-1"}); err == nil {
		t.Fatal("Process() succeeded for transient attempt read error")
	}
	if queue.acks != 0 {
		t.Fatalf("acks=%d, want 0", queue.acks)
	}
}

func TestWorkerDoesNotAcknowledgeWhenTerminalStateCommitFails(t *testing.T) {
	queue := &workerQueueStub{}
	repository := newWorkerRepositoryStub()
	repository.completeErr = errors.New("database unavailable")
	provider := &workerProviderStub{err: errors.New("provider rejected")}
	worker := NewWorker(queue, WorkerDependencies{Repository: repository, Destinations: workerDestinationStub{}, Provider: provider})

	if err := worker.Process(t.Context(), QueueMessage{AttemptID: "attempt-1", Receipt: "receipt-1"}); err == nil {
		t.Fatal("Process() succeeded for terminal state write failure")
	}
	if provider.calls != 1 || repository.completed != 1 || queue.acks != 0 {
		t.Fatalf("provider calls=%d completes=%d acks=%d, want 1, 1, 0", provider.calls, repository.completed, queue.acks)
	}
}

func TestWorkerPassesAttemptIDAsExplicitProviderIdempotencyKey(t *testing.T) {
	queue := &workerQueueStub{}
	repository := newWorkerRepositoryStub()
	provider := &workerProviderStub{}
	worker := NewWorker(queue, WorkerDependencies{Repository: repository, Destinations: workerDestinationStub{}, Provider: provider})

	if err := worker.Process(t.Context(), QueueMessage{AttemptID: "attempt-1", Receipt: "receipt-1"}); err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if provider.request.ProviderIdempotencyKey != repository.attempt.ID {
		t.Fatalf("provider idempotency key = %q, want %q", provider.request.ProviderIdempotencyKey, repository.attempt.ID)
	}
}

func TestWorkerDoesNotReplayNonIdempotentProviderAfterCrash(t *testing.T) {
	queue := &workerQueueStub{}
	repository := newWorkerRepositoryStub()
	repository.attempt.Status = AttemptStatusSending
	repository.claimErr = domain.ErrConflict
	provider := &workerProviderStub{}
	worker := NewWorker(queue, WorkerDependencies{Repository: repository, Destinations: workerDestinationStub{}, Provider: provider})

	if err := worker.Process(t.Context(), QueueMessage{AttemptID: "attempt-1", Receipt: "receipt-1"}); err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if provider.calls != 0 || repository.completed != 1 || repository.completedCode == nil || *repository.completedCode != "delivery_unknown" || queue.acks != 1 {
		t.Fatalf("calls=%d completes=%d code=%v acks=%d, want 0, 1, delivery_unknown, 1", provider.calls, repository.completed, repository.completedCode, queue.acks)
	}
}
