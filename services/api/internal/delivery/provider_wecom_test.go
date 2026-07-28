package delivery

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestWeComBotProviderPostsMarkdownToVerifiedWebhook(t *testing.T) {
	var gotRequest *http.Request
	var gotBody string
	provider := &WeComBotProvider{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		gotRequest = request
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		gotBody = string(body)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"errcode":0,"errmsg":"ok"}`)), Header: make(http.Header)}, nil
	})}}

	err := provider.Send(context.Background(), SendRequest{
		Message:                Message{ID: "msg-1", Channel: ChannelWeComBot, Turns: []FinalTurnSnapshot{{SourceText: "你好", TranslatedText: "hello"}}},
		Attempt:                DeliveryAttempt{ID: "attempt-1"},
		Destination:            VerifiedDestination{Channel: ChannelWeComBot, ProviderTarget: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret"},
		ProviderIdempotencyKey: "attempt-1",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotRequest == nil || gotRequest.Header.Get("X-Lingow-Attempt-ID") != "attempt-1" {
		t.Fatalf("unexpected request: %#v", gotRequest)
	}
	for _, want := range []string{`"msgtype":"markdown"`, "你好", "hello"} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("payload %q does not contain %q", gotBody, want)
		}
	}
}

func TestWeComBotProviderRejectsUntrustedWebhookAndBusinessFailure(t *testing.T) {
	provider := NewWeComBotProvider()
	request := SendRequest{Message: Message{Channel: ChannelWeComBot}, Destination: VerifiedDestination{ProviderTarget: "https://example.com/webhook?key=secret"}, ProviderIdempotencyKey: "attempt-1"}
	if err := provider.Send(context.Background(), request); err == nil {
		t.Fatal("Send() accepted an untrusted webhook host")
	}

	provider.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"errcode":93000,"errmsg":"rate limited"}`)), Header: make(http.Header)}, nil
	})}
	request.Destination.ProviderTarget = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret"
	if err := provider.Send(context.Background(), request); err == nil {
		t.Fatal("Send() accepted an enterprise WeChat business failure")
	}
}

func TestWeComBotProviderDoesNotFollowRedirects(t *testing.T) {
	provider := NewWeComBotProvider()
	calls := 0
	provider.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://untrusted.example.invalid/webhook"}},
			Body:       io.NopCloser(strings.NewReader("redirect")),
		}, nil
	})

	err := provider.Send(context.Background(), SendRequest{
		Message:                Message{ID: "msg-redirect", Channel: ChannelWeComBot},
		Destination:            VerifiedDestination{Channel: ChannelWeComBot, ProviderTarget: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret"},
		ProviderIdempotencyKey: "attempt-redirect",
	})
	if err == nil {
		t.Fatal("Send() accepted a redirect response")
	}
	if calls != 1 {
		t.Fatalf("provider requests = %d, want 1; redirect must not be followed", calls)
	}
}

func TestWeComBotProviderVerificationPostsConfirmation(t *testing.T) {
	calls := 0
	provider := &WeComBotProvider{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Header.Get("X-Lingow-Attempt-ID") != "wecom_bot_configuration" {
			t.Fatalf("attempt header = %q", request.Header.Get("X-Lingow-Attempt-ID"))
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"errcode":0}`)), Header: make(http.Header)}, nil
	})}}

	if err := provider.VerifyWeComBotDestination(context.Background(), "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret"); err != nil {
		t.Fatalf("VerifyWeComBotDestination() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("verification calls = %d, want 1", calls)
	}
}
