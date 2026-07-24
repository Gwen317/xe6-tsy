package accounts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
)

const testTokenSecret = "0123456789abcdef0123456789abcdef"

func TestHMACTokenManagerIssuesAndVerifiesAccessToken(t *testing.T) {
	issuer, verifier, err := NewHMACTokenManager(testTokenSecret, "lingow-api", time.Hour)
	if err != nil {
		t.Fatalf("NewHMACTokenManager() error = %v", err)
	}
	manager := issuer.(*hmacTokenManager)
	now := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }

	tokens, err := issuer.Issue(context.Background(), Account{ID: "account-1"}, Session{ID: "session-1", AccountID: "account-1"})
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	accountID, err := verifier.VerifyAccessToken(context.Background(), tokens.AccessToken)
	if err != nil {
		t.Fatalf("VerifyAccessToken() error = %v", err)
	}
	if accountID != "account-1" {
		t.Fatalf("account ID = %q, want account-1", accountID)
	}
	if tokens.RefreshToken == "" || issuer.HashRefreshToken(tokens.RefreshToken) == tokens.RefreshToken {
		t.Fatal("refresh token or its hash is invalid")
	}
}

func TestHMACTokenManagerRejectsInvalidTokens(t *testing.T) {
	issuer, verifier, err := NewHMACTokenManager(testTokenSecret, "lingow-api", time.Hour)
	if err != nil {
		t.Fatalf("NewHMACTokenManager() error = %v", err)
	}
	manager := issuer.(*hmacTokenManager)
	now := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	tokens, err := issuer.Issue(context.Background(), Account{ID: "account-1"}, Session{ID: "session-1", AccountID: "account-1"})
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	tests := []struct {
		name  string
		token string
		move  time.Duration
	}{
		{name: "malformed", token: "not-a-jwt"},
		{name: "tampered", token: tokens.AccessToken[:len(tokens.AccessToken)-1] + "x"},
		{name: "expired", token: tokens.AccessToken, move: 2 * time.Hour},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager.now = func() time.Time { return now.Add(test.move) }
			_, err := verifier.VerifyAccessToken(context.Background(), test.token)
			if !errors.Is(err, domain.ErrUnauthorized) {
				t.Fatalf("VerifyAccessToken() error = %v, want unauthorized", err)
			}
		})
	}
}

func TestHMACTokenManagerRejectsWeakConfiguration(t *testing.T) {
	_, _, err := NewHMACTokenManager(strings.Repeat("x", 31), "lingow-api", time.Hour)
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("NewHMACTokenManager() error = %v, want invalid argument", err)
	}
}
