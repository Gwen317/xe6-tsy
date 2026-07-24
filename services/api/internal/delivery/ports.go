package delivery

import (
	"context"
	"time"
)

type QueueMessage struct {
	AttemptID string
	Receipt   string
}

type Repository interface {
	// GetIdempotencyResult reads a result scoped by account, operation, and key.
	// Callers compare RequestFingerprint before returning the stored Message.
	GetIdempotencyResult(context.Context, string, IdempotencyOperation, string) (IdempotencyResult, bool, error)
	// CreateMessage atomically persists the idempotency result, message, initial
	// attempt, and outbox record. A concurrent matching request returns the
	// original Message; a different fingerprint returns domain.ErrConflict.
	CreateMessage(context.Context, CreateMessageRecord) (Message, error)
	GetMessage(context.Context, string, string) (Message, error)
	// CreateRetry applies the same atomic idempotency rules while persisting the
	// next attempt, message state, and outbox record.
	CreateRetry(context.Context, CreateRetryRecord) (Message, error)
	// ClaimAttempt atomically changes a queued attempt and its message to sending.
	// claimed=false means another worker already owns or completed this attempt.
	ClaimAttempt(context.Context, string, time.Time) (AttemptWork, bool, error)
	// CompleteAttempt atomically completes the current attempt and optionally
	// creates the next attempt plus its outbox record.
	CompleteAttempt(context.Context, AttemptCompletion) error
	ListPreferences(context.Context, string) ([]Preference, error)
	// PutPreference updates only user-controlled fields and returns the complete
	// preference, including the unchanged authoritative verification state.
	PutPreference(context.Context, UpdatePreferenceRecord) (Preference, error)
}

type TurnReader interface {
	ReadFinalTurns(context.Context, string, []string) ([]FinalTurnSnapshot, error)
}

// DestinationReader is implemented by an adapter over the accounts module.
type DestinationReader interface {
	ResolveVerifiedDestination(context.Context, string, Channel, string) (VerifiedDestination, error)
}

type Provider interface {
	Send(context.Context, SendRequest) error
}

type ProviderFailure interface {
	error
	Code() string
	Retryable() bool
}

type Queue interface {
	Enqueue(context.Context, string, string) error // attempt ID, idempotency key
	Receive(context.Context) (QueueMessage, error)
	Ack(context.Context, string) error
	Nack(context.Context, string, time.Time) error
}

type Service interface {
	Create(context.Context, CreateInput) (Message, error)
	Get(context.Context, string, string) (Message, error)
	Retry(context.Context, string, string, string) (Message, error)
	Preferences(context.Context, string) ([]Preference, error)
	PutPreference(context.Context, string, Channel, bool) (Preference, error)
}
