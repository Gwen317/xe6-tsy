package delivery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
)

type deliveryRepositoryFake struct {
	created       CreateMessageRecord
	createResult  Message
	message       Message
	retry         CreateRetryRecord
	retryResult   Message
	work          AttemptWork
	claimed       bool
	claimErr      error
	completion    AttemptCompletion
	completionErr error
	preferences   []Preference
	putPreference Preference
}

func (f *deliveryRepositoryFake) CreateMessage(_ context.Context, record CreateMessageRecord) (Message, error) {
	f.created = record
	if f.createResult.ID != "" {
		return f.createResult, nil
	}
	return record.Message, nil
}
func (f *deliveryRepositoryFake) GetMessage(context.Context, string, string) (Message, error) {
	return f.message, nil
}
func (f *deliveryRepositoryFake) CreateRetry(_ context.Context, record CreateRetryRecord) (Message, error) {
	f.retry = record
	return f.retryResult, nil
}
func (f *deliveryRepositoryFake) ClaimAttempt(context.Context, string, time.Time) (AttemptWork, bool, error) {
	return f.work, f.claimed, f.claimErr
}
func (f *deliveryRepositoryFake) CompleteAttempt(_ context.Context, completion AttemptCompletion) error {
	f.completion = completion
	return f.completionErr
}
func (f *deliveryRepositoryFake) ListPreferences(context.Context, string) ([]Preference, error) {
	return f.preferences, nil
}
func (f *deliveryRepositoryFake) PutPreference(_ context.Context, preference Preference) (Preference, error) {
	f.putPreference = preference
	return preference, nil
}

type turnReaderFake struct {
	turns []FinalTurnSnapshot
	err   error
}

func (f turnReaderFake) ReadFinalTurns(context.Context, string, []string) ([]FinalTurnSnapshot, error) {
	return f.turns, f.err
}

type destinationReaderFake struct {
	destination VerifiedDestination
	err         error
}

func (f destinationReaderFake) ResolveVerifiedDestination(context.Context, string, Channel, string) (VerifiedDestination, error) {
	return f.destination, f.err
}

func finalTurn(id string) FinalTurnSnapshot {
	return FinalTurnSnapshot{
		TurnID:         id,
		SessionID:      "session-1",
		SourceLanguage: "zh-CN",
		TargetLanguage: "en-US",
		SourceText:     "source " + id,
		TranslatedText: "translation " + id,
		CreatedAt:      time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC),
	}
}

func configuredDeliveryService(repository Repository, turns TurnReader, destinations DestinationReader) *UseCases {
	service := NewService(repository, turns, destinations)
	service.now = func() time.Time { return time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC) }
	ids := []string{"message-1", "attempt-1", "attempt-2"}
	service.newID = func(string) (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	return service
}

func TestCreateBuildsImmutableSnapshotAndAtomicRecord(t *testing.T) {
	repository := &deliveryRepositoryFake{preferences: []Preference{{Channel: ChannelEmail, Enabled: true, Verified: true}}}
	service := configuredDeliveryService(
		repository,
		turnReaderFake{turns: []FinalTurnSnapshot{finalTurn("turn-2"), finalTurn("turn-1")}},
		destinationReaderFake{destination: VerifiedDestination{
			AccountID:      "account-1",
			Channel:        ChannelEmail,
			DestinationRef: "verified-email",
			ProviderTarget: "provider-target",
		}},
	)

	message, err := service.Create(context.Background(), CreateInput{
		AccountID:      "account-1",
		IdempotencyKey: "create-1",
		Channel:        ChannelEmail,
		DestinationRef: "verified-email",
		TurnIDs:        []string{"turn-1", "turn-2"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if message.ID != "message-1" || message.Status != MessageStatusQueued || message.SnapshotVersion != 1 {
		t.Fatalf("Create() = %#v", message)
	}
	if len(message.Turns) != 2 || message.Turns[0].TurnID != "turn-1" || message.Turns[1].TurnID != "turn-2" {
		t.Fatalf("turn order = %#v", message.Turns)
	}
	if repository.created.InitialAttempt.MessageID != message.ID || repository.created.InitialAttempt.AttemptNumber != 1 || repository.created.IdempotencyKey != "create-1" {
		t.Fatalf("atomic record = %#v", repository.created)
	}
	if repository.created.Message.Turns[0].SourceText == "" {
		t.Fatal("message did not preserve turn text snapshot")
	}
}

func TestCreateRejectsUnverifiedDestinationOrIncompleteTurns(t *testing.T) {
	tests := []struct {
		name        string
		destination VerifiedDestination
		turns       []FinalTurnSnapshot
	}{
		{
			name:        "destination belongs to another account",
			destination: VerifiedDestination{AccountID: "other", Channel: ChannelEmail, DestinationRef: "verified-email", ProviderTarget: "target"},
			turns:       []FinalTurnSnapshot{finalTurn("turn-1")},
		},
		{
			name:        "turn reader omitted a requested turn",
			destination: VerifiedDestination{AccountID: "account-1", Channel: ChannelEmail, DestinationRef: "verified-email", ProviderTarget: "target"},
			turns:       nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &deliveryRepositoryFake{preferences: []Preference{{Channel: ChannelEmail, Enabled: true, Verified: true}}}
			service := configuredDeliveryService(repository, turnReaderFake{turns: test.turns}, destinationReaderFake{destination: test.destination})
			_, err := service.Create(context.Background(), CreateInput{
				AccountID: "account-1", IdempotencyKey: "key", Channel: ChannelEmail,
				DestinationRef: "verified-email", TurnIDs: []string{"turn-1"},
			})
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("Create() error = %v, want forbidden", err)
			}
			if repository.created.Message.ID != "" {
				t.Fatal("invalid message reached repository")
			}
		})
	}
}

func TestCreateRejectsDisabledOrUnverifiedChannel(t *testing.T) {
	tests := []struct {
		name       string
		preference Preference
	}{
		{name: "disabled", preference: Preference{Channel: ChannelEmail, Verified: true}},
		{name: "unverified", preference: Preference{Channel: ChannelEmail, Enabled: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &deliveryRepositoryFake{preferences: []Preference{test.preference}}
			service := configuredDeliveryService(
				repository,
				turnReaderFake{turns: []FinalTurnSnapshot{finalTurn("turn-1")}},
				destinationReaderFake{destination: VerifiedDestination{
					AccountID: "account-1", Channel: ChannelEmail, DestinationRef: "verified-email", ProviderTarget: "target",
				}},
			)

			_, err := service.Create(context.Background(), CreateInput{
				AccountID: "account-1", IdempotencyKey: "key", Channel: ChannelEmail,
				DestinationRef: "verified-email", TurnIDs: []string{"turn-1"},
			})
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("Create() error = %v, want forbidden", err)
			}
		})
	}
}

func TestRetryOnlyCreatesNextAttemptForRetryableMessageState(t *testing.T) {
	repository := &deliveryRepositoryFake{
		message:     Message{ID: "message-1", AccountID: "account-1", Status: MessageStatusFailed, Attempts: 1},
		retryResult: Message{ID: "message-1", AccountID: "account-1", Status: MessageStatusRetrying, Attempts: 1},
	}
	service := configuredDeliveryService(repository, turnReaderFake{}, destinationReaderFake{})

	message, err := service.Retry(context.Background(), "account-1", "message-1", "retry-1")
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if message.Status != MessageStatusRetrying || repository.retry.Attempt.AttemptNumber != 2 || repository.retry.IdempotencyKey != "retry-1" {
		t.Fatalf("Retry() = %#v; record=%#v", message, repository.retry)
	}

	repository.message.Status = MessageStatusSent
	_, err = service.Retry(context.Background(), "account-1", "message-1", "retry-2")
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Retry() sent message error = %v, want conflict", err)
	}
}
