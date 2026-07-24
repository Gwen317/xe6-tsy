package accounts

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
)

const minimumHMACSecretBytes = 32

type hmacTokenManager struct {
	secret    []byte
	issuer    string
	accessTTL time.Duration
	now       func() time.Time
}

type accessTokenClaims struct {
	AccountID string `json:"account_id"`
	ExpiresAt int64  `json:"exp"`
	IssuedAt  int64  `json:"iat"`
	Issuer    string `json:"iss"`
	SessionID string `json:"sid"`
	Subject   string `json:"sub"`
}

type tokenHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
}

func NewHMACTokenManager(secret, issuer string, accessTTL time.Duration) (TokenIssuer, AccessTokenVerifier, error) {
	if len(secret) < minimumHMACSecretBytes || issuer == "" || accessTTL <= 0 {
		return nil, nil, domain.ErrInvalidArgument
	}
	manager := &hmacTokenManager{
		secret:    []byte(secret),
		issuer:    issuer,
		accessTTL: accessTTL,
		now:       time.Now,
	}
	return manager, manager, nil
}

func (m *hmacTokenManager) Issue(_ context.Context, account Account, session Session) (Tokens, error) {
	if account.ID == "" || session.ID == "" || session.AccountID != account.ID {
		return Tokens{}, domain.ErrInvalidArgument
	}

	now := m.now().UTC()
	expiresAt := now.Add(m.accessTTL)
	claims := accessTokenClaims{
		AccountID: account.ID,
		ExpiresAt: expiresAt.Unix(),
		IssuedAt:  now.Unix(),
		Issuer:    m.issuer,
		SessionID: session.ID,
		Subject:   account.ID,
	}
	accessToken, err := m.sign(claims)
	if err != nil {
		return Tokens{}, err
	}
	refreshBytes := make([]byte, 32)
	if _, err := rand.Read(refreshBytes); err != nil {
		return Tokens{}, err
	}

	return Tokens{
		AccessToken:  accessToken,
		RefreshToken: base64.RawURLEncoding.EncodeToString(refreshBytes),
		ExpiresAt:    expiresAt,
	}, nil
}

func (m *hmacTokenManager) HashRefreshToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func (m *hmacTokenManager) VerifyAccessToken(_ context.Context, token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", domain.ErrUnauthorized
	}

	signed := parts[0] + "." + parts[1]
	providedSignature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", domain.ErrUnauthorized
	}
	expectedSignature := m.signature(signed)
	if !hmac.Equal(providedSignature, expectedSignature) {
		return "", domain.ErrUnauthorized
	}

	var header tokenHeader
	if err := decodeTokenPart(parts[0], &header); err != nil || header.Algorithm != "HS256" || header.Type != "JWT" {
		return "", domain.ErrUnauthorized
	}
	var claims accessTokenClaims
	if err := decodeTokenPart(parts[1], &claims); err != nil {
		return "", domain.ErrUnauthorized
	}
	now := m.now().UTC().Unix()
	if claims.Issuer != m.issuer || claims.Subject == "" || claims.Subject != claims.AccountID || claims.SessionID == "" || claims.ExpiresAt <= now || claims.IssuedAt > now {
		return "", domain.ErrUnauthorized
	}
	return claims.AccountID, nil
}

func (m *hmacTokenManager) sign(claims accessTokenClaims) (string, error) {
	headerPart, err := encodeTokenPart(tokenHeader{Algorithm: "HS256", Type: "JWT"})
	if err != nil {
		return "", err
	}
	claimsPart, err := encodeTokenPart(claims)
	if err != nil {
		return "", err
	}
	signed := headerPart + "." + claimsPart
	return signed + "." + base64.RawURLEncoding.EncodeToString(m.signature(signed)), nil
}

func (m *hmacTokenManager) signature(value string) []byte {
	mac := hmac.New(sha256.New, m.secret)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

func encodeTokenPart(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeTokenPart(encoded string, target any) error {
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return domain.ErrUnauthorized
	}
	return nil
}
