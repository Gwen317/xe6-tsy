package delivery

import (
	"context"
	"errors"
)

var ErrProviderNotConfigured = errors.New("provider_not_configured")

// UnconfiguredProvider fails closed until an approved outbound provider is
// configured. It must not report a delivery as successful merely because the
// infrastructure is reachable.
type UnconfiguredProvider struct{}

func (UnconfiguredProvider) Send(context.Context, SendRequest) error { return ErrProviderNotConfigured }

var _ Provider = UnconfiguredProvider{}
