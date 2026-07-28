package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	recordsv1 "github.com/1024XEngineer/xe6-tsy/packages/contracts/records/v1"
	"github.com/1024XEngineer/xe6-tsy/services/api/internal/accounts"
	"github.com/1024XEngineer/xe6-tsy/services/api/internal/delivery"
	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
	"github.com/1024XEngineer/xe6-tsy/services/api/internal/usage"
	internalwebapi "github.com/1024XEngineer/xe6-tsy/services/api/internal/webapi"
	"github.com/1024XEngineer/xe6-tsy/services/api/languages"
	recordswebapi "github.com/1024XEngineer/xe6-tsy/services/api/webapi"
)

type buildMuxTokenVerifier struct{}

func (buildMuxTokenVerifier) VerifyAccessToken(_ context.Context, token string) (accounts.AccessTokenClaims, error) {
	if token != "access-token" {
		return accounts.AccessTokenClaims{}, domain.ErrUnauthorized
	}
	return accounts.AccessTokenClaims{AccountID: "acct_01", SessionID: "auths_01"}, nil
}

func TestBuildMuxRegistersVoiceRecordRoutes(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		system bool
	}{
		{name: "list participants", method: http.MethodGet, path: "/api/v1/voice-sessions/vs_01/participants"},
		{name: "update participant", method: http.MethodPatch, path: "/api/v1/voice-sessions/vs_01/participants/p_01", body: `{"voice_profile_id":null}`, system: true},
		{name: "list session turns", method: http.MethodGet, path: "/api/v1/voice-sessions/vs_01/turns"},
		{name: "get turn", method: http.MethodGet, path: "/api/v1/voice-turns/vt_01"},
		{name: "correct attribution", method: http.MethodPatch, path: "/api/v1/voice-turns/vt_01/attribution", body: `{"participant_id":"p_01","attribution_status":"corrected"}`, system: true},
		{name: "list history", method: http.MethodGet, path: "/api/v1/translation-history"},
	}

	handler := buildMux(languages.NewHandler(nil, nil))
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var body io.Reader
			if test.body != "" {
				body = strings.NewReader(test.body)
			}
			request := httptest.NewRequest(test.method, test.path, body)
			ctx := internalwebapi.WithAccountID(request.Context(), "acct_01")
			if test.system {
				ctx = recordswebapi.WithSystemActor(ctx)
			}
			request = request.WithContext(ctx)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusNotImplemented {
				t.Fatalf("status = %d, want %d, body = %s", response.Code, http.StatusNotImplemented, response.Body.String())
			}
			var errorResponse recordsv1.ErrorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &errorResponse); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if errorResponse.Error.Code != recordsv1.ErrorNotImplemented {
				t.Fatalf("error code = %q, want %q", errorResponse.Error.Code, recordsv1.ErrorNotImplemented)
			}
		})
	}
}

func TestBuildMuxRegistersLanguageRoutes(t *testing.T) {
	handler := buildMux(languages.NewHandler(nil, func(*http.Request) (string, bool) {
		return "acct_01", true
	}))

	request := httptest.NewRequest(http.MethodGet, "/api/v1/languages", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	// Nil service keeps language routes registered but explicitly unimplemented.
	if response.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d, body = %s", response.Code, http.StatusNotImplemented, response.Body.String())
	}
}

func TestPersistentBuildMuxAuthenticatesSharedModuleRoutes(t *testing.T) {
	runtime := &persistentRuntime{
		accounts: accounts.NewUseCases(),
		verifier: buildMuxTokenVerifier{},
		usage:    usage.NewUseCases(),
		delivery: delivery.NewUseCases(),
	}
	handler := buildMux(languages.NewHandler(nil, func(r *http.Request) (string, bool) {
		return internalwebapi.AccountIDFromContext(r.Context())
	}), runtime)

	unauthenticated := httptest.NewRequest(http.MethodGet, "/api/v1/languages", nil)
	unauthenticated.Header.Set("X-Account-ID", "forged-account")
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d; body=%s", unauthenticatedResponse.Code, http.StatusUnauthorized, unauthenticatedResponse.Body.String())
	}

	authenticated := httptest.NewRequest(http.MethodGet, "/api/v1/languages", nil)
	authenticated.Header.Set("Authorization", "Bearer access-token")
	authenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(authenticatedResponse, authenticated)
	if authenticatedResponse.Code != http.StatusNotImplemented {
		t.Fatalf("authenticated status = %d, want %d; body=%s", authenticatedResponse.Code, http.StatusNotImplemented, authenticatedResponse.Body.String())
	}

	// Public authentication endpoints remain reachable without a bearer token;
	// the protected subtree must not shadow their more-specific patterns.
	publicAuth := httptest.NewRequest(http.MethodPost, "/api/v1/auth/anonymous", nil)
	publicAuthResponse := httptest.NewRecorder()
	handler.ServeHTTP(publicAuthResponse, publicAuth)
	if publicAuthResponse.Code != http.StatusNotImplemented {
		t.Fatalf("public auth status = %d, want %d; body=%s", publicAuthResponse.Code, http.StatusNotImplemented, publicAuthResponse.Body.String())
	}
}
