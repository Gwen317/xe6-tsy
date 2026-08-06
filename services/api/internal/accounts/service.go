package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/1024XEngineer/xe6-tsy/services/api/authcontext"
	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
)

// UseCases 是账户与登录模块的业务编排层。
// 它只依赖端口，不感知 HTTP、PostgreSQL 或短信供应商的具体实现；缺少必需依赖时明确返回 not_implemented。
type UseCases struct {
	repository         Repository          // 保存账户、验证码挑战和可撤销登录会话。
	issuer             TokenIssuer         // 签发 Access/Refresh Token，并生成 Refresh Token 摘要。
	verifier           AccessTokenVerifier // 校验受保护接口携带的 Access Token。
	sender             VerificationSender  // 将验证码交付到手机号，具体供应商位于适配层。
	digester           *CredentialDigester // 生成手机号和验证码的认证专用摘要。
	verificationPolicy VerificationPolicy  // 仅本地或测试环境使用的固定验证码策略。
}

var (
	canonicalPhonePattern   = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)
	verificationCodePattern = regexp.MustCompile(`^[0-9]{6}$`)
)

const challengeRestoreTimeout = 3 * time.Second

// NewUseCases 创建未接入外部依赖的安全关闭实现，主要用于测试路由和未启用功能的部署。
// 调用需要持久化或签发凭证的方法时会返回 not_implemented，而不会伪造成功数据。
func NewUseCases() *UseCases { return &UseCases{} }

// NewPersistentUseCases 将账户业务策略连接到持久化、Token、验证码发送和摘要适配器。
// digester 使用可选参数是为了兼容旧装配代码；登录验证码链路没有 digester 时仍会安全关闭。
func NewPersistentUseCases(repository Repository, issuer TokenIssuer, verifier AccessTokenVerifier, sender VerificationSender, digesters ...*CredentialDigester) *UseCases {
	var digester *CredentialDigester
	if len(digesters) > 0 {
		digester = digesters[0]
	}
	return &UseCases{repository: repository, issuer: issuer, verifier: verifier, sender: sender, digester: digester}
}

// WithVerificationPolicy 配置本地开发所需的固定验证码行为；生产环境应保持为空并生成随机验证码。
func (u *UseCases) WithVerificationPolicy(policy VerificationPolicy) *UseCases {
	if u != nil {
		u.verificationPolicy = policy
	}
	return u
}

// CreateAnonymous 为首次访问用户创建临时账户，并立即建立一条可撤销登录会话。
// 匿名账户不是无身份状态：后续会话、记录和用量都先归属于该账户，手机号登录时再迁移归属。
func (u *UseCases) CreateAnonymous(ctx context.Context) (AuthResult, error) {
	if u.repository == nil || u.issuer == nil {
		return AuthResult{}, domain.ErrNotImplemented
	}
	account, err := u.repository.CreateAnonymous(ctx)
	if err != nil {
		return AuthResult{}, err
	}
	return u.issueSession(ctx, account)
}

// CreatePhoneChallenge 创建一次短期手机号验证挑战。
// 数据库先原子执行手机号维度的冷却和滚动限流，再发送验证码；发送失败也保留挑战和限流记录，
// 因为外部供应商超时时可能已经实际接收请求，立即删除会允许攻击者绕过发送频率限制。
func (u *UseCases) CreatePhoneChallenge(ctx context.Context, phone string) (string, error) {
	if u.repository == nil || u.sender == nil || u.digester == nil {
		return "", domain.ErrNotImplemented
	}
	if !canonicalPhonePattern.MatchString(phone) {
		return "", domain.ErrInvalidArgument
	}
	code, err := u.generateVerificationCode()
	if err != nil {
		return "", fmt.Errorf("generate verification code: %w", err)
	}
	challengeID, err := randomID()
	if err != nil {
		return "", fmt.Errorf("generate challenge ID: %w", err)
	}
	now := time.Now().UTC()
	id := "challenge_" + challengeID
	legacyRateLimitHash := hashValue(phone)
	legacyPhoneHash, err := u.digester.EncryptLegacyPhoneHash(legacyRateLimitHash)
	if err != nil {
		return "", fmt.Errorf("protect legacy phone lookup: %w", err)
	}
	challenge := PhoneChallenge{
		ID: id, PhoneHash: u.digester.PhoneHash(phone), LegacyPhoneHash: legacyPhoneHash, LegacyRateLimitHash: legacyRateLimitHash,
		CodeHash: u.digester.CodeHash(id, code), DigestVersion: 2,
		ExpiresAt: now.Add(10 * time.Minute), CreatedAt: now, MaxAttempts: defaultPhoneChallengeMaxAttempts,
	}
	if err := u.repository.CreateChallenge(ctx, challenge); err != nil {
		return "", err
	}
	if err := u.sender.SendCode(ctx, phone, code); err != nil {
		// 发送失败具有不确定性：供应商可能已接收请求，只是响应超时。
		// 因此保留挑战，让每次发送尝试都受冷却时间和滚动配额约束。
		return "", err
	}
	return challenge.ID, nil
}

// VerifyPhone 完成手机号验证码登录，并可选择把调用者拥有的匿名账户合并到正式账户。
// 正确验证码会先以一次性方式消费；如果后续账户或会话写入失败，defer 会恢复该验证码，
// 避免瞬时数据库故障永久烧掉用户已经正确输入的验证码。错误验证码不会进入恢复路径。
func (u *UseCases) VerifyPhone(ctx context.Context, challengeID, code, anonymousAccountID string) (result AuthResult, err error) {
	if u.repository == nil || u.issuer == nil || u.digester == nil {
		return AuthResult{}, domain.ErrNotImplemented
	}
	if challengeID == "" || !verificationCodePattern.MatchString(NormalizeVerificationCode(code)) {
		return AuthResult{}, domain.ErrInvalidArgument
	}
	if err := verifyAnonymousBindingOwnership(ctx, anonymousAccountID); err != nil {
		return AuthResult{}, err
	}
	challenge, err := u.repository.ConsumeChallenge(ctx, challengeID, u.verificationCodeHash(challengeID, code))
	if err != nil {
		return AuthResult{}, err
	}
	completed := false
	defer func() {
		if !completed {
			// 验证码消费单独提交，是为了让错误尝试次数立即持久化。
			// 这里只恢复已经正确消费、但账户或会话落库失败的挑战。
			restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), challengeRestoreTimeout)
			restoreErr := u.repository.RestoreChallenge(restoreCtx, challenge.ID)
			cancel()
			if restoreErr != nil {
				err = fmt.Errorf("complete phone verification: %w", errors.Join(err, fmt.Errorf("restore consumed challenge: %w", restoreErr)))
			}
		}
	}()
	// Repository 根据受保护的手机号摘要解析正式账户，公开模型和日志始终不携带手机号摘要。
	legacyPhoneHash, err := u.digester.DecryptLegacyPhoneHash(challenge.LegacyPhoneHash)
	if err != nil {
		return AuthResult{}, domain.ErrUnauthorized
	}
	challengeAccount, err := u.repository.FindOrCreateByPhoneHashes(ctx, challenge.PhoneHash, legacyPhoneHash)
	if err != nil {
		return AuthResult{}, err
	}
	if anonymousAccountID != "" {
		// 先为正式账户准备新会话，再由 Repository 在同一事务中完成匿名账户合并和会话写入。
		// 任一步失败都会回滚，避免业务数据已经迁移但客户端没有可用登录凭证。
		session, tokens, prepareErr := u.prepareSession(ctx, challengeAccount)
		if prepareErr != nil {
			return AuthResult{}, prepareErr
		}
		challengeAccount, err = u.repository.BindAnonymousAndCreateSession(ctx, anonymousAccountID, challengeAccount.ID, session)
		if err != nil {
			return AuthResult{}, err
		}
		completed = true
		return AuthResult{Account: challengeAccount, Tokens: tokens}, nil
	}
	result, err = u.issueSession(ctx, challengeAccount)
	if err != nil {
		return AuthResult{}, err
	}
	completed = true
	return result, nil
}

// verifyAnonymousBindingOwnership 保证“合并匿名账户”是一个需要认证的操作。
// 仅手机号登录可以保持公开；一旦请求携带 anonymousAccountID，就必须同时提供属于该匿名账户的
// 有效 Access Token。该校验同时存在于 HTTP 层和用例层，防止内部调用绕过账户归属边界。
func verifyAnonymousBindingOwnership(ctx context.Context, anonymousAccountID string) error {
	if anonymousAccountID == "" {
		return nil
	}
	accountID, ok := authcontext.AccountID(ctx)
	if !ok {
		return domain.ErrUnauthorized
	}
	if accountID != anonymousAccountID {
		return domain.ErrForbidden
	}
	return nil
}

// Refresh 使用 Refresh Token 摘要找到活动会话，并通过“撤销旧会话 + 创建后继会话”完成轮换。
// Repository 必须原子执行轮换，使同一 Refresh Token 的并发或重放请求最多只有一个成功。
func (u *UseCases) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	if u.repository == nil || u.issuer == nil {
		return Tokens{}, domain.ErrNotImplemented
	}
	if refreshToken == "" {
		return Tokens{}, domain.ErrInvalidArgument
	}
	session, err := u.repository.GetSessionByRefreshHash(ctx, u.issuer.HashRefreshToken(refreshToken))
	if err != nil {
		return Tokens{}, mapCredentialLookupError(err)
	}
	account, err := u.repository.GetAccount(ctx, session.AccountID)
	if err != nil {
		return Tokens{}, err
	}
	successor, tokens, err := u.prepareSession(ctx, account)
	if err != nil {
		return Tokens{}, err
	}
	if err := u.repository.RotateSession(ctx, session.ID, successor); err != nil {
		return Tokens{}, mapCredentialLookupError(err)
	}
	return tokens, nil
}

// Logout 撤销 Refresh Token 所属登录会话。
// Access Token 校验还会查询会话活动状态，因此撤销后无需等待 JWT 自然过期即可阻止继续访问。
func (u *UseCases) Logout(ctx context.Context, refreshToken string) error {
	if u.repository == nil || u.issuer == nil {
		return domain.ErrNotImplemented
	}
	if refreshToken == "" {
		return domain.ErrInvalidArgument
	}
	session, err := u.repository.GetSessionByRefreshHash(ctx, u.issuer.HashRefreshToken(refreshToken))
	if err != nil {
		return mapCredentialLookupError(err)
	}
	return mapCredentialLookupError(u.repository.RevokeSession(ctx, session.ID))
}

// Me 根据认证中间件写入的可信 accountID 返回当前账户，不从请求体接收账户身份。
func (u *UseCases) Me(ctx context.Context, accountID string) (Account, error) {
	if u.repository == nil {
		return Account{}, domain.ErrNotImplemented
	}
	if accountID == "" {
		return Account{}, domain.ErrUnauthorized
	}
	return u.repository.GetAccount(ctx, accountID)
}

// VerifyAccessToken 把 Token 解析、签名和会话状态校验统一交给 verifier。
// 只有 verifier 返回的 AccountID 才能进入后续业务上下文，客户端提交的账户字段不可信。
func (u *UseCases) VerifyAccessToken(ctx context.Context, token string) (AccessTokenClaims, error) {
	if u.verifier == nil {
		return AccessTokenClaims{}, domain.ErrNotImplemented
	}
	return u.verifier.VerifyAccessToken(ctx, token)
}

// issueSession 先生成凭证和登录会话，再持久化 Refresh Token 摘要。
// 只有会话保存成功才把明文凭证返回客户端，避免返回无法刷新或撤销的悬空 Token。
func (u *UseCases) issueSession(ctx context.Context, account Account) (AuthResult, error) {
	session, tokens, err := u.prepareSession(ctx, account)
	if err != nil {
		return AuthResult{}, err
	}
	if err := u.repository.CreateSession(ctx, session); err != nil {
		return AuthResult{}, err
	}
	return AuthResult{Account: account, Tokens: tokens}, nil
}

func (u *UseCases) generateVerificationCode() (string, error) {
	if u.verificationPolicy.enabled() {
		return NormalizeVerificationCode(u.verificationPolicy.UniversalCode), nil
	}
	return randomDigits(6)
}

func (u *UseCases) verificationCodeHash(challengeID, code string) string {
	normalized := NormalizeVerificationCode(code)
	if u.verificationPolicy.enabled() {
		universal := NormalizeVerificationCode(u.verificationPolicy.UniversalCode)
		if normalized == universal {
			return u.digester.CodeHash(challengeID, universal)
		}
	}
	return u.digester.CodeHash(challengeID, normalized)
}

// prepareSession 生成新的登录会话 ID 和凭证，但不执行数据库写入。
// 调用者可将返回的 Session 放入更大的事务，例如匿名账户合并与首次正式登录。
func (u *UseCases) prepareSession(ctx context.Context, account Account) (Session, Tokens, error) {
	now := time.Now().UTC()
	sessionID, err := randomID()
	if err != nil {
		return Session{}, Tokens{}, fmt.Errorf("generate session ID: %w", err)
	}
	session := Session{ID: "auths_" + sessionID, AccountID: account.ID, CreatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour)}
	tokens, err := u.issuer.Issue(ctx, account, session)
	if err != nil {
		return Session{}, Tokens{}, err
	}
	session.RefreshHash = u.issuer.HashRefreshToken(tokens.RefreshToken)
	return session, tokens, nil
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func randomDigits(length int) (string, error) {
	digits := make([]byte, length)
	random := make([]byte, length)
	for written := 0; written < length; {
		if _, err := rand.Read(random); err != nil {
			return "", err
		}
		// 250 是小于 256 的最大十的倍数；丢弃 250-255 可避免取模导致某些数字出现概率更高。
		for _, value := range random {
			if value >= 250 {
				continue
			}
			digits[written] = '0' + value%10
			written++
			if written == length {
				break
			}
		}
	}
	return string(digits), nil
}

func hashValue(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// mapCredentialLookupError 将不存在、过期或已经轮换的 Refresh Token 统一映射为 unauthorized。
// 凭证不是可枚举的公开资源，不能用 404 暴露某个 Refresh Token 是否曾经存在。
func mapCredentialLookupError(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrUnauthorized
	}
	return err
}
