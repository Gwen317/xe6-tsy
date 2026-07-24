package usage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
)

type repositoryFake struct {
	recorded       RecordInput
	detail         Detail
	alreadyExists  bool
	sessionSummary Summary
	accountSummary Summary
}

func (f *repositoryFake) Record(_ context.Context, input RecordInput) (Detail, bool, error) {
	f.recorded = input
	return f.detail, f.alreadyExists, nil
}
func (f *repositoryFake) SessionSummary(context.Context, string, string) (Summary, error) {
	return f.sessionSummary, nil
}
func (f *repositoryFake) AccountSummary(context.Context, string, time.Time, time.Time) (Summary, error) {
	return f.accountSummary, nil
}

type sessionOwnerFake struct {
	accountID string
	err       error
}

func (f sessionOwnerFake) AccountIDForSession(context.Context, string) (string, error) {
	return f.accountID, f.err
}

func validTranslationRecord() RecordInput {
	return RecordInput{
		EventVersion:   UsageEventVersion,
		ID:             "usage-1",
		TraceID:        "trace-1",
		IdempotencyKey: "usage-1",
		AccountID:      "account-1",
		SessionID:      "session-1",
		TurnID:         "turn-1",
		ServiceType:    StageTranslation,
		Provider:       "provider",
		Model:          "model",
		InputTokens:    10,
		OutputTokens:   8,
		CostAmount:     "0.010000",
		Currency:       "CNY",
		OccurredAt:     time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC),
	}
}

func TestRecordValidatesOwnershipAndPreservesIdempotentResult(t *testing.T) {
	input := validTranslationRecord()
	want := Detail{RecordInput: input, RecordedAt: input.OccurredAt.Add(time.Second)}
	repository := &repositoryFake{detail: want, alreadyExists: true}
	service := NewService(repository, sessionOwnerFake{accountID: "account-1"})

	got, err := service.Record(context.Background(), input)
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if got.RecordedAt != want.RecordedAt || repository.recorded.IdempotencyKey != "usage-1" {
		t.Fatalf("Record() = %#v; recorded=%#v", got, repository.recorded)
	}
}

func TestRecordRejectsInvalidFactsBeforePersistence(t *testing.T) {
	tests := []struct {
		name   string
		change func(*RecordInput)
	}{
		{name: "unknown version", change: func(input *RecordInput) { input.EventVersion = 2 }},
		{name: "negative tokens", change: func(input *RecordInput) { input.InputTokens = -1 }},
		{name: "translation audio", change: func(input *RecordInput) { input.AudioDurationMS = 1 }},
		{name: "missing currency", change: func(input *RecordInput) { input.Currency = "" }},
		{name: "invalid amount", change: func(input *RecordInput) { input.CostAmount = "1e3" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validTranslationRecord()
			test.change(&input)
			repository := &repositoryFake{}
			service := NewService(repository, sessionOwnerFake{accountID: "account-1"})

			_, err := service.Record(context.Background(), input)
			if !errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("Record() error = %v, want invalid argument", err)
			}
			if repository.recorded.ID != "" {
				t.Fatal("invalid fact reached repository")
			}
		})
	}
}

func TestRecordRejectsMismatchedSessionOwner(t *testing.T) {
	repository := &repositoryFake{}
	service := NewService(repository, sessionOwnerFake{accountID: "another-account"})

	_, err := service.Record(context.Background(), validTranslationRecord())
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("Record() error = %v, want forbidden", err)
	}
	if repository.recorded.ID != "" {
		t.Fatal("mismatched fact reached repository")
	}
}

func TestSessionUsageRejectsCrossAccountRead(t *testing.T) {
	service := NewService(&repositoryFake{}, sessionOwnerFake{accountID: "owner-account"})

	_, err := service.SessionUsage(context.Background(), "request-account", "session-1")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("SessionUsage() error = %v, want forbidden", err)
	}
}
