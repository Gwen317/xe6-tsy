// Package identity defines stable domain error categories for identity capabilities.
package identity

import "errors"

var (
	// TODO(identity-errors): Keep only errors used by Service and mapped safely by the HTTP layer.
	// Never expose raw provider errors, stacks, or sensitive details to API callers.
	ErrNotImplemented       = errors.New("identity capability not implemented")
	ErrInvalidAssertion     = errors.New("invalid identity assertion")
	ErrAuthenticationFailed = errors.New("identity authentication failed")
	ErrAccessDenied         = errors.New("identity access denied")
	ErrProviderUnavailable  = errors.New("identity provider unavailable")
)
