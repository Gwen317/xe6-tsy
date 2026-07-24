package usage

import (
	"context"
	"regexp"
	"time"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
)

var decimalAmountPattern = regexp.MustCompile(`^(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)

type UseCases struct {
	repository Repository
	sessions   SessionOwnerReader
}

func NewUseCases() *UseCases { return &UseCases{} }

func NewService(repository Repository, sessions SessionOwnerReader) *UseCases {
	return &UseCases{repository: repository, sessions: sessions}
}

func (u *UseCases) Record(ctx context.Context, input RecordInput) (Detail, error) {
	if u.repository == nil || u.sessions == nil {
		return Detail{}, domain.ErrNotImplemented
	}
	if !validRecord(input) {
		return Detail{}, domain.ErrInvalidArgument
	}
	ownerID, err := u.sessions.AccountIDForSession(ctx, input.SessionID)
	if err != nil {
		return Detail{}, err
	}
	if ownerID != input.AccountID {
		return Detail{}, domain.ErrForbidden
	}
	detail, _, err := u.repository.Record(ctx, input)
	return detail, err
}

func (u *UseCases) SessionUsage(ctx context.Context, accountID, sessionID string) (Summary, error) {
	if u.repository == nil || u.sessions == nil {
		return Summary{}, domain.ErrNotImplemented
	}
	if accountID == "" || sessionID == "" {
		return Summary{}, domain.ErrInvalidArgument
	}
	ownerID, err := u.sessions.AccountIDForSession(ctx, sessionID)
	if err != nil {
		return Summary{}, err
	}
	if ownerID != accountID {
		return Summary{}, domain.ErrForbidden
	}
	return u.repository.SessionSummary(ctx, accountID, sessionID)
}

func (u *UseCases) AccountUsage(ctx context.Context, accountID string, start, end time.Time) (Summary, error) {
	if u.repository == nil {
		return Summary{}, domain.ErrNotImplemented
	}
	if accountID == "" || start.IsZero() || end.IsZero() || !start.Before(end) {
		return Summary{}, domain.ErrInvalidArgument
	}
	return u.repository.AccountSummary(ctx, accountID, start, end)
}

func validRecord(input RecordInput) bool {
	if input.EventVersion != UsageEventVersion || input.ID == "" || input.TraceID == "" || input.IdempotencyKey == "" || input.AccountID == "" || input.SessionID == "" || input.TurnID == "" || input.Provider == "" || input.Model == "" || input.OccurredAt.IsZero() {
		return false
	}
	if input.InputTokens < 0 || input.OutputTokens < 0 || input.AudioDurationMS < 0 {
		return false
	}
	if (input.CostAmount == "") != (input.Currency == "") {
		return false
	}
	if input.CostAmount != "" && (!decimalAmountPattern.MatchString(input.CostAmount) || len(input.Currency) != 3 || input.Currency[0] < 'A' || input.Currency[0] > 'Z' || input.Currency[1] < 'A' || input.Currency[1] > 'Z' || input.Currency[2] < 'A' || input.Currency[2] > 'Z') {
		return false
	}
	switch input.ServiceType {
	case StageTranslation:
		return input.AudioDurationMS == 0
	case StageASR, StageTTS, StageDiarization:
		return input.InputTokens == 0 && input.OutputTokens == 0
	default:
		return false
	}
}
