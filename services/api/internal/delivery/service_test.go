package delivery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
)

type deliveryRepositoryFake struct {
	created             CreateMessageRecord
	createResult        Message
	createErr           error
	message             Message
	getMessageCalls     int
	retry               CreateRetryRecord
	retryResult         Message
	retryErr            error
	idempotencyResult   IdempotencyResult
	idempotencyFound    bool
	idempotencyErr      error
	idempotencyLookup   func(string, IdempotencyOperation, string) (IdempotencyResult, bool, error)
	idempotencyCalls    int
	work                AttemptWork
	claimed             bool
	claimErr            error
	completion          AttemptCompletion
	completionErr       error
	preferences         []Preference
	preferenceListCalls int
	preferenceUpdate    UpdatePreferenceRecord
	preferenceResult    Preference
}

func (f *deliveryRepositoryFake) CreateMessage(_ context.Context, record CreateMessageRecord) (Message, error) {
	f.created = record
	if f.createErr != nil {
		return Message{}, f.createErr
	}
	if f.createResult.ID != "" {
		return f.createResult, nil
	}
	return record.Message, nil
}
func (f *deliveryRepositoryFake) GetMessage(context.Context, string, string) (Message, error) {
	f.getMessageCalls++
	return f.message, nil
}
func (f *deliveryRepositoryFake) CreateRetry(_ context.Context, record CreateRetryRecord) (Message, error) {
	f.retry = record
	return f.retryResult, f.retryErr
}
func (f *deliveryRepositoryFake) GetIdempotencyResult(_ context.Context, accountID string, operation IdempotencyOperation, key string) (IdempotencyResult, bool, error) {
	f.idempotencyCalls++
	if f.idempotencyLookup != nil {
		return f.idempotencyLookup(accountID, operation, key)
	}
	return f.idempotencyResult, f.idempotencyFound, f.idempotencyErr
}
func (f *deliveryRepositoryFake) ClaimAttempt(context.Context, string, time.Time) (AttemptWork, bool, error) {
	return f.work, f.claimed, f.claimErr
}
func (f *deliveryRepositoryFake) CompleteAttempt(_ context.Context, completion AttemptCompletion) error {
	f.completion = completion
	return f.completionErr
}
func (f *deliveryRepositoryFake) ListPreferences(context.Context, string) ([]Preference, error) {
	f.preferenceListCalls++
	return f.preferences, nil
}
func (f *deliveryRepositoryFake) PutPreference(_ context.Context, update UpdatePreferenceRecord) (Preference, error) {
	f.preferenceUpdate = update
	if f.preferenceResult.AccountID != "" {
		return f.preferenceResult, nil
	}
	return Preference{
		AccountID: update.AccountID,
		Channel:   update.Channel,
		Enabled:   update.Enabled,
		UpdatedAt: update.UpdatedAt,
	}, nil
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
	if repository.created.InitialAttempt.MessageID != message.ID || repository.created.InitialAttempt.AttemptNumber != 1 || repository.created.IdempotencyKey != "create-1" || repository.created.RequestFingerprint == "" {
		t.Fatalf("atomic record = %#v", repository.created)
	}
	if repository.created.Message.Turns[0].SourceText == "" {
		t.Fatal("message did not preserve turn text snapshot")
	}
}

func TestCreateReplaysResultBeforeMutableBusinessChecks(t *testing.T) {
	input := CreateInput{
		AccountID:      "account-1",
		IdempotencyKey: "create-1",
		Channel:        ChannelEmail,
		DestinationRef: "verified-email",
		TurnIDs:        []string{"turn-1"},
	}
	fingerprint, err := createRequestFingerprint(input)
	if err != nil {
		t.Fatalf("createRequestFingerprint() error = %v", err)
	}
	want := Message{ID: "original-message", AccountID: "account-1", Status: MessageStatusQueued}
	repository := &deliveryRepositoryFake{
		idempotencyResult: IdempotencyResult{RequestFingerprint: fingerprint, Message: want},
		idempotencyFound:  true,
	}
	service := configuredDeliveryService(
		repository,
		turnReaderFake{err: domain.ErrForbidden},
		destinationReaderFake{err: domain.ErrForbidden},
	)

	got, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create() replay error = %v", err)
	}
	if got.ID != want.ID || repository.preferenceListCalls != 0 || repository.created.Message.ID != "" {
		t.Fatalf("Create() replay = %#v, preference calls = %d, created = %#v", got, repository.preferenceListCalls, repository.created)
	}
}

func TestCreateRejectsReusedKeyWithDifferentRequest(t *testing.T) {
	original := CreateInput{Channel: ChannelEmail, DestinationRef: "verified-email", TurnIDs: []string{"turn-1"}}
	fingerprint, err := createRequestFingerprint(original)
	if err != nil {
		t.Fatalf("createRequestFingerprint() error = %v", err)
	}
	repository := &deliveryRepositoryFake{
		idempotencyResult: IdempotencyResult{RequestFingerprint: fingerprint, Message: Message{ID: "original-message"}},
		idempotencyFound:  true,
	}
	service := configuredDeliveryService(repository, turnReaderFake{}, destinationReaderFake{})

	_, err = service.Create(context.Background(), CreateInput{
		AccountID: "account-1", IdempotencyKey: "create-1", Channel: ChannelEmail,
		DestinationRef: "verified-email", TurnIDs: []string{"turn-2"},
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Create() reused key error = %v, want conflict", err)
	}
	if repository.preferenceListCalls != 0 {
		t.Fatalf("mutable checks ran %d times", repository.preferenceListCalls)
	}
}

func TestCreateLoadsConcurrentWinningResult(t *testing.T) {
	input := CreateInput{
		AccountID: "account-1", IdempotencyKey: "create-1", Channel: ChannelEmail,
		DestinationRef: "verified-email", TurnIDs: []string{"turn-1"},
	}
	fingerprint, err := createRequestFingerprint(input)
	if err != nil {
		t.Fatalf("createRequestFingerprint() error = %v", err)
	}
	winner := Message{ID: "winning-message", AccountID: "account-1", Status: MessageStatusQueued}
	repository := &deliveryRepositoryFake{
		createErr:   domain.ErrConflict,
		preferences: []Preference{{Channel: ChannelEmail, Enabled: true, Verified: true}},
	}
	repository.idempotencyLookup = func(string, IdempotencyOperation, string) (IdempotencyResult, bool, error) {
		if repository.idempotencyCalls == 1 {
			return IdempotencyResult{}, false, nil
		}
		return IdempotencyResult{RequestFingerprint: fingerprint, Message: winner}, true, nil
	}
	service := configuredDeliveryService(
		repository,
		turnReaderFake{turns: []FinalTurnSnapshot{finalTurn("turn-1")}},
		destinationReaderFake{destination: VerifiedDestination{
			AccountID: "account-1", Channel: ChannelEmail, DestinationRef: "verified-email", ProviderTarget: "target",
		}},
	)

	got, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create() concurrent replay error = %v", err)
	}
	if got.ID != winner.ID || repository.idempotencyCalls != 2 {
		t.Fatalf("Create() = %#v, idempotency calls = %d", got, repository.idempotencyCalls)
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

func TestPutPreferencePreservesAuthoritativeVerificationState(t *testing.T) {
	wantTime := time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC)
	repository := &deliveryRepositoryFake{
		preferenceResult: Preference{
			AccountID: "account-1",
			Channel:   ChannelEmail,
			Enabled:   false,
			Verified:  true,
			UpdatedAt: wantTime,
		},
	}
	service := configuredDeliveryService(repository, turnReaderFake{}, destinationReaderFake{})

	got, err := service.PutPreference(context.Background(), "account-1", ChannelEmail, false)
	if err != nil {
		t.Fatalf("PutPreference() error = %v", err)
	}
	if !got.Verified {
		t.Fatal("PutPreference() cleared verified state")
	}
	if repository.preferenceUpdate.AccountID != "account-1" || repository.preferenceUpdate.Channel != ChannelEmail || repository.preferenceUpdate.Enabled || !repository.preferenceUpdate.UpdatedAt.Equal(wantTime) {
		t.Fatalf("preference update = %#v", repository.preferenceUpdate)
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
	if message.Status != MessageStatusRetrying || repository.retry.Attempt.AttemptNumber != 2 || repository.retry.IdempotencyKey != "retry-1" || repository.retry.RequestFingerprint == "" {
		t.Fatalf("Retry() = %#v; record=%#v", message, repository.retry)
	}

	repository.message.Status = MessageStatusSent
	_, err = service.Retry(context.Background(), "account-1", "message-1", "retry-2")
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Retry() sent message error = %v, want conflict", err)
	}
}

func TestRetryReplaysResultBeforeReadingCurrentMessageState(t *testing.T) {
	fingerprint, err := retryRequestFingerprint("message-1")
	if err != nil {
		t.Fatalf("retryRequestFingerprint() error = %v", err)
	}
	want := Message{ID: "message-1", AccountID: "account-1", Status: MessageStatusRetrying}
	repository := &deliveryRepositoryFake{
		message:           Message{ID: "message-1", Status: MessageStatusSent},
		idempotencyResult: IdempotencyResult{RequestFingerprint: fingerprint, Message: want},
		idempotencyFound:  true,
	}
	service := configuredDeliveryService(repository, turnReaderFake{}, destinationReaderFake{})

	got, err := service.Retry(context.Background(), "account-1", "message-1", "retry-1")
	if err != nil {
		t.Fatalf("Retry() replay error = %v", err)
	}
	if got.Status != want.Status || repository.getMessageCalls != 0 || repository.retry.Attempt.ID != "" {
		t.Fatalf("Retry() replay = %#v, message reads = %d, retry = %#v", got, repository.getMessageCalls, repository.retry)
	}
}

func TestRetryRejectsReusedKeyForDifferentMessage(t *testing.T) {
	fingerprint, err := retryRequestFingerprint("message-1")
	if err != nil {
		t.Fatalf("retryRequestFingerprint() error = %v", err)
	}
	repository := &deliveryRepositoryFake{
		idempotencyResult: IdempotencyResult{RequestFingerprint: fingerprint, Message: Message{ID: "message-1"}},
		idempotencyFound:  true,
	}
	service := configuredDeliveryService(repository, turnReaderFake{}, destinationReaderFake{})

	_, err = service.Retry(context.Background(), "account-1", "message-2", "retry-1")
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Retry() reused key error = %v, want conflict", err)
	}
	if repository.getMessageCalls != 0 {
		t.Fatalf("current message was read %d times", repository.getMessageCalls)
	}
}

func TestRetryLoadsConcurrentWinningResult(t *testing.T) {
	fingerprint, err := retryRequestFingerprint("message-1")
	if err != nil {
		t.Fatalf("retryRequestFingerprint() error = %v", err)
	}
	winner := Message{ID: "message-1", AccountID: "account-1", Status: MessageStatusRetrying}
	repository := &deliveryRepositoryFake{
		message:  Message{ID: "message-1", AccountID: "account-1", Status: MessageStatusFailed, Attempts: 1},
		retryErr: domain.ErrConflict,
	}
	repository.idempotencyLookup = func(string, IdempotencyOperation, string) (IdempotencyResult, bool, error) {
		if repository.idempotencyCalls == 1 {
			return IdempotencyResult{}, false, nil
		}
		return IdempotencyResult{RequestFingerprint: fingerprint, Message: winner}, true, nil
	}
	service := configuredDeliveryService(repository, turnReaderFake{}, destinationReaderFake{})

	got, err := service.Retry(context.Background(), "account-1", "message-1", "retry-1")
	if err != nil {
		t.Fatalf("Retry() concurrent replay error = %v", err)
	}
	if got.Status != winner.Status || repository.idempotencyCalls != 2 {
		t.Fatalf("Retry() = %#v, idempotency calls = %d", got, repository.idempotencyCalls)
	}
}
