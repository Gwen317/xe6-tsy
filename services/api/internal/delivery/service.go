package delivery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
)

const (
	messageIDPrefix = "message_"
	attemptIDPrefix = "attempt_"
	maxAttempts     = 3
)

type UseCases struct {
	repository   Repository
	turns        TurnReader
	destinations DestinationReader
	now          func() time.Time
	newID        func(string) (string, error)
}

func NewUseCases() *UseCases { return &UseCases{} }

func NewService(repository Repository, turns TurnReader, destinations DestinationReader) *UseCases {
	return &UseCases{
		repository:   repository,
		turns:        turns,
		destinations: destinations,
		now:          time.Now,
		newID:        secureID,
	}
}

func (u *UseCases) Create(ctx context.Context, input CreateInput) (Message, error) {
	if u.repository == nil || u.turns == nil || u.destinations == nil || u.now == nil || u.newID == nil {
		return Message{}, domain.ErrNotImplemented
	}
	if !validCreateInput(input) {
		return Message{}, domain.ErrInvalidArgument
	}
	preferences, err := u.repository.ListPreferences(ctx, input.AccountID)
	if err != nil {
		return Message{}, err
	}
	if !channelEnabled(preferences, input.Channel) {
		return Message{}, domain.ErrForbidden
	}
	destination, err := u.destinations.ResolveVerifiedDestination(ctx, input.AccountID, input.Channel, input.DestinationRef)
	if err != nil {
		return Message{}, err
	}
	if destination.AccountID != input.AccountID || destination.Channel != input.Channel || destination.DestinationRef != input.DestinationRef || destination.ProviderTarget == "" {
		return Message{}, domain.ErrForbidden
	}
	turns, err := u.turns.ReadFinalTurns(ctx, input.AccountID, input.TurnIDs)
	if err != nil {
		return Message{}, err
	}
	orderedTurns, ok := validateAndOrderTurns(turns, input.TurnIDs)
	if !ok {
		return Message{}, domain.ErrForbidden
	}
	messageID, err := u.newID(messageIDPrefix)
	if err != nil {
		return Message{}, err
	}
	attemptID, err := u.newID(attemptIDPrefix)
	if err != nil {
		return Message{}, err
	}
	now := u.now().UTC()
	message := Message{
		ID:              messageID,
		AccountID:       input.AccountID,
		Channel:         input.Channel,
		DestinationRef:  input.DestinationRef,
		SnapshotVersion: 1,
		Turns:           orderedTurns,
		Status:          MessageStatusQueued,
		Attempts:        0,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	attempt := DeliveryAttempt{
		ID:            attemptID,
		MessageID:     messageID,
		AttemptNumber: 1,
		Status:        AttemptStatusQueued,
		CreatedAt:     now,
	}
	return u.repository.CreateMessage(ctx, CreateMessageRecord{
		Message:        message,
		InitialAttempt: attempt,
		IdempotencyKey: input.IdempotencyKey,
	})
}

func (u *UseCases) Get(ctx context.Context, accountID, messageID string) (Message, error) {
	if u.repository == nil {
		return Message{}, domain.ErrNotImplemented
	}
	if accountID == "" || messageID == "" {
		return Message{}, domain.ErrInvalidArgument
	}
	return u.repository.GetMessage(ctx, accountID, messageID)
}

func (u *UseCases) Retry(ctx context.Context, accountID, messageID, idempotencyKey string) (Message, error) {
	if u.repository == nil || u.now == nil || u.newID == nil {
		return Message{}, domain.ErrNotImplemented
	}
	if accountID == "" || messageID == "" || idempotencyKey == "" {
		return Message{}, domain.ErrInvalidArgument
	}
	message, err := u.repository.GetMessage(ctx, accountID, messageID)
	if err != nil {
		return Message{}, err
	}
	if message.Status != MessageStatusFailed || message.Attempts >= maxAttempts {
		return Message{}, domain.ErrConflict
	}
	attemptID, err := u.newID(attemptIDPrefix)
	if err != nil {
		return Message{}, err
	}
	attempt := DeliveryAttempt{
		ID:            attemptID,
		MessageID:     message.ID,
		AttemptNumber: message.Attempts + 1,
		Status:        AttemptStatusQueued,
		CreatedAt:     u.now().UTC(),
	}
	return u.repository.CreateRetry(ctx, CreateRetryRecord{
		AccountID:      accountID,
		MessageID:      messageID,
		Attempt:        attempt,
		IdempotencyKey: idempotencyKey,
	})
}

func (u *UseCases) Preferences(ctx context.Context, accountID string) ([]Preference, error) {
	if u.repository == nil {
		return nil, domain.ErrNotImplemented
	}
	if accountID == "" {
		return nil, domain.ErrInvalidArgument
	}
	return u.repository.ListPreferences(ctx, accountID)
}

func (u *UseCases) PutPreference(ctx context.Context, accountID string, channel Channel, enabled bool) (Preference, error) {
	if u.repository == nil || u.now == nil {
		return Preference{}, domain.ErrNotImplemented
	}
	if accountID == "" || channel != ChannelEmail {
		return Preference{}, domain.ErrInvalidArgument
	}
	return u.repository.PutPreference(ctx, Preference{
		AccountID: accountID,
		Channel:   channel,
		Enabled:   enabled,
		UpdatedAt: u.now().UTC(),
	})
}

func validCreateInput(input CreateInput) bool {
	if input.AccountID == "" || input.IdempotencyKey == "" || input.Channel != ChannelEmail || input.DestinationRef == "" || len(input.TurnIDs) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(input.TurnIDs))
	for _, id := range input.TurnIDs {
		if id == "" {
			return false
		}
		if _, exists := seen[id]; exists {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func validateAndOrderTurns(turns []FinalTurnSnapshot, requestedIDs []string) ([]FinalTurnSnapshot, bool) {
	if len(turns) != len(requestedIDs) {
		return nil, false
	}
	byID := make(map[string]FinalTurnSnapshot, len(turns))
	for _, turn := range turns {
		if turn.TurnID == "" || turn.SessionID == "" || turn.SourceLanguage == "" || turn.TargetLanguage == "" || turn.SourceText == "" || turn.TranslatedText == "" || turn.CreatedAt.IsZero() {
			return nil, false
		}
		if _, exists := byID[turn.TurnID]; exists {
			return nil, false
		}
		byID[turn.TurnID] = turn
	}
	ordered := make([]FinalTurnSnapshot, 0, len(requestedIDs))
	for _, id := range requestedIDs {
		turn, exists := byID[id]
		if !exists {
			return nil, false
		}
		ordered = append(ordered, turn)
	}
	return ordered, true
}

func channelEnabled(preferences []Preference, channel Channel) bool {
	for _, preference := range preferences {
		if preference.Channel == channel {
			return preference.Enabled && preference.Verified
		}
	}
	return false
}

func secureID(prefix string) (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(bytes), nil
}
