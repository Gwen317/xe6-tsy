package delivery

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
	"unicode/utf8"
)

const (
	weComBotHost         = "qyapi.weixin.qq.com"
	weComBotPath         = "/cgi-bin/webhook/send"
	weComMarkdownMaxSize = 3800
	weComBotTimeout      = 10 * time.Second
)

// WeComBotProvider posts immutable message snapshots to a verified enterprise
// WeChat group-bot webhook. The webhook URL is supplied only by the encrypted
// destination reader immediately before this call.
//
// Enterprise WeChat group bots do not expose a documented idempotency-key
// parameter. The provider therefore deliberately does not implement
// IdempotentProvider: a crashed in-flight attempt is recorded as
// delivery_unknown instead of automatically posting a duplicate group message.
type WeComBotProvider struct{ client *http.Client }

type weComBotPayload struct {
	MessageType string `json:"msgtype"`
	Markdown    struct {
		Content string `json:"content"`
	} `json:"markdown"`
}

type weComBotResponse struct {
	ErrorCode *int   `json:"errcode"`
	ErrorText string `json:"errmsg"`
}

func NewWeComBotProvider() *WeComBotProvider {
	return &WeComBotProvider{client: &http.Client{
		Timeout: weComBotTimeout,
		// Destination validation applies to the actual request. Following a 3xx
		// response would let a verified enterprise WeChat URL redirect the
		// immutable message body to an unverified host.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// VerifyWeComBotDestination posts a harmless configuration confirmation before
// the encrypted target is accepted for an account.
func (p *WeComBotProvider) VerifyWeComBotDestination(ctx context.Context, target string) error {
	return p.Send(ctx, SendRequest{
		Message:                Message{ID: "wecom_bot_configuration", Channel: ChannelWeComBot},
		Attempt:                DeliveryAttempt{ID: "wecom_bot_configuration"},
		Destination:            VerifiedDestination{Channel: ChannelWeComBot, ProviderTarget: target},
		ProviderIdempotencyKey: "wecom_bot_configuration",
	})
}

func (p *WeComBotProvider) Send(ctx context.Context, request SendRequest) error {
	if p == nil || p.client == nil || request.Message.Channel != ChannelWeComBot || request.ProviderIdempotencyKey == "" {
		return fmt.Errorf("invalid enterprise WeChat delivery request")
	}
	if err := validateWeComBotWebhook(request.Destination.ProviderTarget); err != nil {
		return err
	}
	payload := weComBotPayload{MessageType: "markdown"}
	payload.Markdown.Content = formatWeComMarkdown(request.Message, request.ProviderIdempotencyKey)
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal enterprise WeChat payload: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, request.Destination.ProviderTarget, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create enterprise WeChat request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	// The group-bot endpoint does not promise to consume this header. It still
	// lets a trusted gateway or audit proxy correlate the durable attempt.
	httpRequest.Header.Set("X-Lingow-Attempt-ID", request.ProviderIdempotencyKey)

	response, err := p.client.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("post enterprise WeChat webhook: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return fmt.Errorf("enterprise WeChat webhook returned HTTP %d", response.StatusCode)
	}
	var result weComBotResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result); err != nil {
		return fmt.Errorf("decode enterprise WeChat webhook response: %w", err)
	}
	if result.ErrorCode == nil {
		return fmt.Errorf("enterprise WeChat webhook response is missing errcode")
	}
	if *result.ErrorCode != 0 {
		return fmt.Errorf("enterprise WeChat webhook rejected message: errcode=%d", *result.ErrorCode)
	}
	return nil
}

func validateWeComBotWebhook(rawURL string) error {
	endpoint, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() != weComBotHost || endpoint.EscapedPath() != weComBotPath || endpoint.Query().Get("key") == "" || endpoint.User != nil {
		return fmt.Errorf("invalid enterprise WeChat bot webhook")
	}
	return nil
}

func formatWeComMarkdown(message Message, attemptID string) string {
	var content strings.Builder
	content.WriteString("## Lingow 翻译消息\n")
	content.WriteString("> 消息：`" + message.ID + "`\n")
	content.WriteString("> 投递：`" + attemptID + "`\n")
	for index, turn := range message.Turns {
		content.WriteString("\n**片段 ")
		content.WriteString(fmt.Sprintf("%d", index+1))
		content.WriteString("**\n")
		content.WriteString("原文：")
		content.WriteString(turn.SourceText)
		content.WriteString("\n译文：")
		content.WriteString(turn.TranslatedText)
		content.WriteString("\n")
	}
	return truncateUTF8(content.String(), weComMarkdownMaxSize)
}

func truncateUTF8(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	if maximum < len("...") {
		return ""
	}
	limit := maximum - len("...")
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit] + "..."
}

var _ Provider = (*WeComBotProvider)(nil)
