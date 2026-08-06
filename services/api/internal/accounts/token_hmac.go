package accounts

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
)

// HMACIssuer 实现 v1 短期 JWT Access Token 与高熵不透明 Refresh Token 契约。
// Access Token 不只校验签名和过期时间，还通过回调查验数据库中的登录会话状态，
// 因此退出登录或 Refresh Token 轮换后，旧 Access Token 可以在自然过期前立即失效。
type HMACIssuer struct {
	secret           []byte
	issuer           string
	audience         string
	accessTTL        time.Duration
	active           func(context.Context, string) (bool, error)
	activeForAccount func(context.Context, string, string) (bool, error)
}

// NewHMACIssuer 使用只按 session ID 检查活动状态的兼容回调创建签发器。
// 新的生产装配应使用 NewHMACIssuerWithAccount，同时检查 Token subject 与会话当前归属。
func NewHMACIssuer(secret, issuer, audience string, active func(context.Context, string) (bool, error)) (*HMACIssuer, error) {
	if len([]byte(secret)) < 32 || issuer == "" || audience == "" || active == nil {
		return nil, fmt.Errorf("%w: token configuration is incomplete", domain.ErrInvalidArgument)
	}
	return &HMACIssuer{secret: []byte(secret), issuer: issuer, audience: audience, accessTTL: time.Hour, active: active}, nil
}

// NewHMACIssuerWithAccount 使用 session ID 和 account subject 联合检查会话活动状态。
// 匿名账户合并会把既有会话迁移到正式账户；联合检查可以让合并前签发的旧 subject Token 立即失效。
func NewHMACIssuerWithAccount(secret, issuer, audience string, active func(context.Context, string, string) (bool, error)) (*HMACIssuer, error) {
	if len([]byte(secret)) < 32 || issuer == "" || audience == "" || active == nil {
		return nil, fmt.Errorf("%w: token configuration is incomplete", domain.ErrInvalidArgument)
	}
	return &HMACIssuer{secret: []byte(secret), issuer: issuer, audience: audience, accessTTL: time.Hour, activeForAccount: active}, nil
}

// Issue 为指定账户和登录会话签发一小时有效的 HS256 Access Token，并生成独立 Refresh Token。
// JWT 的 sub 绑定账户，sid 绑定可撤销登录会话；Refresh Token 不写入 JWT，也不复用 JWT 密钥材料。
func (i *HMACIssuer) Issue(_ context.Context, account Account, session Session) (Tokens, error) {
	if i == nil || len(i.secret) == 0 || account.ID == "" || session.ID == "" {
		return Tokens{}, domain.ErrInvalidArgument
	}
	now := time.Now().UTC()
	expires := now.Add(i.accessTTL)
	header := encodePart(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload := encodePart(map[string]any{
		"iss": i.issuer, "aud": i.audience, "sub": account.ID, "sid": session.ID,
		"iat": now.Unix(), "exp": expires.Unix(),
	})
	unsigned := header + "." + payload
	refreshToken, err := newRefreshToken()
	if err != nil {
		return Tokens{}, fmt.Errorf("generate refresh token: %w", err)
	}
	return Tokens{AccessToken: unsigned + "." + i.sign(unsigned), RefreshToken: refreshToken, ExpiresAt: expires}, nil
}

// HashRefreshToken 将 Refresh Token 转换为数据库可保存、可等值查询的摘要。
// 服务端后续无法从摘要还原明文，数据库泄漏不会直接暴露可用 Refresh Token。
func (i *HMACIssuer) HashRefreshToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// VerifyAccessToken 依次校验 JWT 结构、常量时间签名、标准声明以及服务端登录会话状态。
// 只有所有检查都通过才返回可信身份；具体失败原因统一隐藏为 unauthorized，避免泄露校验细节。
func (i *HMACIssuer) VerifyAccessToken(ctx context.Context, token string) (AccessTokenClaims, error) {
	if i == nil || len(i.secret) == 0 || i.issuer == "" || i.audience == "" {
		return AccessTokenClaims{}, domain.ErrUnauthorized
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return AccessTokenClaims{}, domain.ErrUnauthorized
	}
	unsigned := parts[0] + "." + parts[1]
	// hmac.Equal 使用常量时间比较，避免签名比较过程暴露时序信息。
	if !hmac.Equal([]byte(parts[2]), []byte(i.sign(unsigned))) {
		return AccessTokenClaims{}, domain.ErrUnauthorized
	}
	var payload struct {
		Issuer   string `json:"iss"`
		Audience string `json:"aud"`
		Subject  string `json:"sub"`
		Session  string `json:"sid"`
		Expires  int64  `json:"exp"`
		Issued   int64  `json:"iat"`
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(decoded, &payload) != nil {
		return AccessTokenClaims{}, domain.ErrUnauthorized
	}
	now := time.Now().Unix()
	// 同时约束签发者、受众、身份、会话、过期时间和未来时间漂移，不能只验证签名。
	if payload.Issuer != i.issuer || payload.Audience != i.audience || payload.Subject == "" || payload.Session == "" || payload.Expires <= now || payload.Issued > now+60 {
		return AccessTokenClaims{}, domain.ErrUnauthorized
	}
	if i.activeForAccount != nil {
		// 生产路径同时检查 session 与 account，阻止账户合并后的旧 subject Token 继续授权。
		active, err := i.activeForAccount(ctx, payload.Session, payload.Subject)
		if err != nil {
			return AccessTokenClaims{}, err
		}
		if !active {
			return AccessTokenClaims{}, domain.ErrUnauthorized
		}
	} else if i.active != nil {
		active, err := i.active(ctx, payload.Session)
		if err != nil {
			return AccessTokenClaims{}, err
		}
		if !active {
			return AccessTokenClaims{}, domain.ErrUnauthorized
		}
	}
	return AccessTokenClaims{AccountID: payload.Subject, SessionID: payload.Session}, nil
}

func (i *HMACIssuer) sign(unsigned string) string {
	digest := hmac.New(sha256.New, i.secret)
	_, _ = digest.Write([]byte(unsigned))
	return base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}

func encodePart(value any) string {
	payload, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(payload)
}

func newRefreshToken() (string, error) {
	// 256 bit 加密随机数保证 Refresh Token 无法通过枚举或预测获得。
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

var _ TokenIssuer = (*HMACIssuer)(nil)
var _ AccessTokenVerifier = (*HMACIssuer)(nil)
