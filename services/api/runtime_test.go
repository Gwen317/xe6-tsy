package main

import "testing"

func TestNewVerificationSenderFromEnvKeepsMockDevelopmentOnly(t *testing.T) {
	t.Setenv("LINGOW_SMS_PROVIDER", "disabled")
	if sender, err := newVerificationSenderFromEnv(); err != nil || sender != nil {
		t.Fatalf("disabled sender = (%T, %v), want (nil, nil)", sender, err)
	}

	t.Setenv("LINGOW_SMS_PROVIDER", "mock-webhook")
	t.Setenv("LINGOW_SMS_WEBHOOK_URL", "http://127.0.0.1:18080/codes")
	t.Setenv("LINGOW_APP_ENV", "production")
	if _, err := newVerificationSenderFromEnv(); err == nil {
		t.Fatal("production mock webhook configuration succeeded")
	}

	t.Setenv("LINGOW_APP_ENV", "development")
	if sender, err := newVerificationSenderFromEnv(); err != nil || sender == nil {
		t.Fatalf("development mock sender = (%T, %v), want configured sender", sender, err)
	}
}
