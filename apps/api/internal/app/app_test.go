package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/1024XEngineer/xe6-tsy/apps/api/internal/config"
)

func TestNewServesHealthCheck(t *testing.T) {
	router := New(config.Config{Mode: "test"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestNewRegistersUnimplementedIdentityAndKnowledgeRoutes(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "start access session", method: http.MethodPost, path: "/api/v1/identity/access-sessions", body: `{}`},
		{name: "get access context", method: http.MethodGet, path: "/api/v1/identity/access-sessions/session-1"},
		{name: "authorize action", method: http.MethodPost, path: "/api/v1/identity/authorizations", body: `{}`},
		{name: "end access session", method: http.MethodPost, path: "/api/v1/identity/access-sessions/session-1/end", body: `{}`},
		{name: "revoke membership", method: http.MethodPost, path: "/api/v1/identity/memberships/membership-1/revocations", body: `{}`},
		{name: "revoke access session", method: http.MethodPost, path: "/api/v1/identity/access-sessions/session-1/revocations", body: `{}`},
		{name: "create knowledge import", method: http.MethodPost, path: "/api/v1/configuration/knowledge/import-jobs", body: `{}`},
		{name: "review knowledge item", method: http.MethodPost, path: "/api/v1/configuration/knowledge/import-items/item-1/reviews", body: `{}`},
		{name: "publish knowledge", method: http.MethodPost, path: "/api/v1/configuration/knowledge/publications", body: `{}`},
		{name: "get knowledge bundle", method: http.MethodGet, path: "/api/v1/configuration/knowledge/bases/base-1/publications/publication-1"},
		{name: "retire knowledge", method: http.MethodPost, path: "/api/v1/configuration/knowledge/publications/publication-1/retirements", body: `{}`},
	}

	router := New(config.Config{Mode: "test"})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != http.StatusNotImplemented {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusNotImplemented, response.Body.String())
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Code != "NOT_IMPLEMENTED" {
				t.Fatalf("error code = %q, want %q", body.Error.Code, "NOT_IMPLEMENTED")
			}
		})
	}
}
