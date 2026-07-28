package accounts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebhookVerificationSenderPostsCodeToReceiver(t *testing.T) {
	var got verificationWebhookRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", request.Method)
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content type = %q, want application/json", request.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	sender, err := NewWebhookVerificationSender(server.URL)
	if err != nil {
		t.Fatalf("NewWebhookVerificationSender() error = %v", err)
	}
	if err := sender.SendCode(context.Background(), "+8613800000000", "123456"); err != nil {
		t.Fatalf("SendCode() error = %v", err)
	}
	if got != (verificationWebhookRequest{Phone: "+8613800000000", Code: "123456"}) {
		t.Fatalf("webhook request = %#v", got)
	}
}

func TestWebhookVerificationSenderRejectsInvalidEndpointAndFailureStatus(t *testing.T) {
	for _, endpoint := range []string{"", "smtp://localhost", "not a URL"} {
		if _, err := NewWebhookVerificationSender(endpoint); err == nil {
			t.Fatalf("NewWebhookVerificationSender(%q) succeeded", endpoint)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	sender, err := NewWebhookVerificationSender(server.URL)
	if err != nil {
		t.Fatalf("NewWebhookVerificationSender() error = %v", err)
	}
	if err := sender.SendCode(context.Background(), "+8613800000000", "123456"); err == nil {
		t.Fatal("SendCode() succeeded for failed receiver")
	}
}
