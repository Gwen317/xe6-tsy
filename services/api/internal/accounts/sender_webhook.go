package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WebhookVerificationSender delivers one-time codes to a development-only
// mock receiver. The receiver owns how the code is displayed; this adapter
// never writes a code to logs or returns it to an API caller.
type WebhookVerificationSender struct {
	endpoint string
	client   *http.Client
}

type verificationWebhookRequest struct {
	Phone string `json:"phone"`
	Code  string `json:"code"`
}

// NewWebhookVerificationSender constructs a sender for a local mock HTTP
// receiver. HTTPS and loopback/local HTTP endpoints are accepted so local
// development can use an unencrypted mock without weakening production paths.
func NewWebhookVerificationSender(endpoint string) (*WebhookVerificationSender, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid verification webhook URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("verification webhook URL must use HTTP or HTTPS")
	}
	return &WebhookVerificationSender{
		endpoint: parsed.String(),
		client: &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// SendCode makes one bounded request to the configured mock receiver. A
// non-2xx result is a delivery failure; callers must not report a challenge as
// delivered when the receiver did not accept it.
func (s *WebhookVerificationSender) SendCode(ctx context.Context, phone, code string) error {
	if s == nil || s.client == nil || s.endpoint == "" {
		return fmt.Errorf("verification webhook sender is not configured")
	}
	payload, err := json.Marshal(verificationWebhookRequest{Phone: phone, Code: code})
	if err != nil {
		return fmt.Errorf("marshal verification webhook request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create verification webhook request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("send verification webhook request: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("verification webhook returned HTTP %d", response.StatusCode)
	}
	return nil
}

var _ VerificationSender = (*WebhookVerificationSender)(nil)
