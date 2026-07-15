// Package identity defines staff identity, access-session, and authorization contracts.
package identity

import (
	"context"
	"time"
)

type RoleCode string

const (
	RoleStaff            RoleCode = "STAFF"
	RoleConfigMaintainer RoleCode = "CONFIG_MAINTAINER"
)

type ScopeType string

const (
	ScopeOrganization ScopeType = "ORGANIZATION"
	ScopeServicePoint ScopeType = "SERVICE_POINT"
	ScopeWindow       ScopeType = "WINDOW"
)

type Decision string

const (
	DecisionAllow Decision = "ALLOW"
	DecisionDeny  Decision = "DENY"
)

type Authenticator interface {
	// TODO(identity-authenticate): Implement through an approved identity-provider adapter, covering
	// timeouts, context cancellation, replay protection, safe error mapping, and tests proving
	// that passwords and raw provider subjects never enter logs, audit events, or persistence.
	Authenticate(context.Context, LoginCredential) (IdentityAssertion, error)
}

type Service interface {
	// TODO(identity-start-access-session): Validate assertion lifetime and nonce, resolve staff
	// membership and scopes, create an expiring session idempotently, and record a safe audit event.
	StartAccessSession(context.Context, IdentityAssertion) (AccessContext, error)

	// TODO(identity-get-access-context): Return only active, scope-matching membership and session
	// data; reject expired, revoked, and cross-organization access without leaking resource existence.
	GetAccessContext(context.Context, GetAccessContextQuery) (AccessContext, error)

	// TODO(identity-authorize-action): Evaluate action, role, organization, service-point, window,
	// and resource scopes with deny-by-default behavior and an auditable policy version and reason.
	AuthorizeAction(context.Context, AuthorizationRequest) (AccessDecision, error)

	// TODO(identity-end-access-session): End sessions idempotently with optimistic concurrency and
	// record an audit event without sensitive data or duplicate side effects.
	EndAccessSession(context.Context, EndAccessSessionCommand) error

	// TODO(identity-revoke-membership): Authorize the actor and organization scope, define related
	// session invalidation, use optimistic concurrency, and prevent cross-organization escalation.
	RevokeMembership(context.Context, RevokeMembershipCommand) error

	// TODO(identity-revoke-access-session): Authorize actor scope, revoke idempotently, handle version conflicts, and retain a complete audit trail.
	RevokeAccessSession(context.Context, RevokeAccessSessionCommand) error
}

type LoginCredential struct {
	Username string
	Password string
}

type IdentityAssertion struct {
	ProviderCode    string
	ExternalSubject string
	IssuedAt        time.Time
	ExpiresAt       time.Time
	Nonce           string
}

type GetAccessContextQuery struct {
	AccessSessionID string
	OrganizationID  string
}

type AuthorizationRequest struct {
	AccessSessionID string
	Action          string
	OrganizationID  string
	ResourceType    string
	ResourceID      string
	ServicePointID  string
	WindowID        string
}

type AccessContext struct {
	OperatorID         string
	OrganizationID     string
	MembershipID       string
	RoleCodes          []RoleCode
	ServicePointScopes []string
	WindowScopes       []string
	AccessSessionID    string
	IssuedAt           time.Time
	ExpiresAt          time.Time
}

type AccessDecision struct {
	Decision      Decision
	ReasonCode    string
	PolicyVersion string
	EvaluatedAt   time.Time
}

type EndAccessSessionCommand struct {
	AccessSessionID string
	ExpectedVersion int64
}

type RevokeMembershipCommand struct {
	MembershipID    string
	ActorID         string
	Reason          string
	ExpectedVersion int64
}

type RevokeAccessSessionCommand struct {
	AccessSessionID string
	ActorID         string
	Reason          string
	ExpectedVersion int64
}
