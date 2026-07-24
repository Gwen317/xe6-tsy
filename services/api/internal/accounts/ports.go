package accounts

import "context"

type Repository interface {
	CreateAnonymous(context.Context) (Account, error)
	GetAccount(context.Context, string) (Account, error)
	CreateChallenge(context.Context, PhoneChallenge) error
	ConsumeChallenge(context.Context, string, string) error
	FindOrCreateByPhoneHash(context.Context, string) (Account, error)
	BindAnonymous(context.Context, string, string) (Account, error)
	CreateSession(context.Context, Session) error
	GetSessionByRefreshHash(context.Context, string) (Session, error)
	RevokeSession(context.Context, string) error
}

type VerificationSender interface {
	SendCode(context.Context, string, string) error
}

type TokenIssuer interface {
	Issue(context.Context, Account, Session) (Tokens, error)
	HashRefreshToken(string) string
}

// AccessTokenVerifier validates a bearer token and returns its trusted account.
// HTTP middleware is the only component that may turn this result into request
// account context.
type AccessTokenVerifier interface {
	VerifyAccessToken(context.Context, string) (string, error)
}

type Service interface {
	CreateAnonymous(context.Context) (AuthResult, error)
	CreatePhoneChallenge(context.Context, string) (string, error)
	VerifyPhone(context.Context, string, string, string) (AuthResult, error)
	Refresh(context.Context, string) (Tokens, error)
	Logout(context.Context, string) error
	Me(context.Context, string) (Account, error)
}
