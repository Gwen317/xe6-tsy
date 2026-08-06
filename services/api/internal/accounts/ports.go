package accounts

import "context"

// Repository 是登录领域访问持久化状态的唯一端口。
// 实现必须保证验证码消费、匿名账户合并和 Refresh Token 轮换所要求的事务与幂等语义。
type Repository interface {
	// CreateAnonymous 创建可以立即拥有会话和业务数据的临时账户身份。
	CreateAnonymous(context.Context) (Account, error)
	// GetAccount 按服务端账户 ID 读取当前有效账户，已经合并的旧账户不应作为活动账户返回。
	GetAccount(context.Context, string) (Account, error)
	// CreateChallenge 在同一事务内执行单手机号冷却时间和滚动窗口限流，再保存短期挑战。
	// 多实例并发请求也必须共享同一个限流结果，不能先发送验证码再补写数据库。
	CreateChallenge(context.Context, PhoneChallenge) error
	// ConsumeChallenge 加锁读取并校验验证码摘要，只在首次成功时返回手机号绑定信息。
	// 错误验证码也必须先持久化增加尝试次数，再返回未授权，防止通过重试绕过次数限制。
	ConsumeChallenge(context.Context, string, string) (PhoneChallenge, error)
	// RestoreChallenge 仅在验证码已正确消费、但后续账户或登录会话写入失败时恢复挑战。
	// 它不能恢复错误验证码，也不能把超过次数或已经过期的挑战重新开放。
	RestoreChallenge(context.Context, string) error
	// FindOrCreateByPhoneHashes 优先用当前 HMAC 摘要查询正式账户，并用旧 SHA-256 摘要完成惰性迁移。
	FindOrCreateByPhoneHashes(context.Context, string, string) (Account, error)
	// BindAnonymous 把匿名账户的归属关系迁移到正式账户。
	// 登录主流程必须使用 BindAnonymousAndCreateSession，使合并与首个正式登录会话原子提交。
	BindAnonymous(context.Context, string, string) (Account, error)
	// BindAnonymousAndCreateSession 在一个事务内迁移匿名账户并保存正式账户登录会话。
	// 任何会话写入失败都必须回滚账户合并，避免用户数据已迁移但客户端没有可用凭证。
	BindAnonymousAndCreateSession(context.Context, string, string, Session) (Account, error)
	// CreateSession 保存一条可以通过 Refresh Token 轮换的登录会话。
	CreateSession(context.Context, Session) error
	// GetSessionByRefreshHash 只通过 Refresh Token 摘要查询未撤销且未过期的会话。
	GetSessionByRefreshHash(context.Context, string) (Session, error)
	// RotateSession 原子撤销当前会话并创建后继会话，保证同一 Refresh Token 只能成功轮换一次。
	// 后继会话写入失败时必须保持原会话有效，客户端才能安全重试。
	RotateSession(context.Context, string, Session) error
	// RevokeSession 撤销 Refresh Token 对应的登录会话，使其刷新链和关联 Access Token 立即失效。
	RevokeSession(context.Context, string) error
}

// VerificationSender 隔离验证码业务策略与短信、日志或测试内存实现。
type VerificationSender interface {
	// SendCode 把一次性验证码发送到目标手机号，API 响应中不得回传验证码明文。
	SendCode(context.Context, string, string) error
}

// TokenIssuer 统一负责 Access Token 签发和 Refresh Token 摘要策略。
type TokenIssuer interface {
	// Issue 为已经确定的账户与登录会话签发一组新凭证。
	Issue(context.Context, Account, Session) (Tokens, error)
	// HashRefreshToken 生成可用于数据库查询和保存的摘要，禁止持久化 Refresh Token 明文。
	HashRefreshToken(string) string
}

// AccessTokenVerifier 在系统信任账户身份前校验 Access Token。
// 实现必须拒绝签名错误、过期、会话已撤销或会话归属已变化的 Token。
type AccessTokenVerifier interface {
	VerifyAccessToken(context.Context, string) (AccessTokenClaims, error)
}

// CanonicalAccountResolver 沿匿名账户合并链解析当前有效的最终账户。
// 用量、记录等读模型在比较归属前使用它，使注册用户仍能访问匿名阶段产生的数据。
type CanonicalAccountResolver interface {
	CanonicalAccountID(context.Context, string) (string, error)
}

// Service 定义 HTTP 适配层可以调用的账户与登录用例，不暴露数据库或 Token 实现细节。
type Service interface {
	// CreateAnonymous 建立临时账户归属并返回第一组登录凭证。
	CreateAnonymous(context.Context) (AuthResult, error)
	// CreatePhoneChallenge 发起手机号验证，只返回不透明 challenge ID。
	CreatePhoneChallenge(context.Context, string) (string, error)
	// VerifyPhone 消费验证码，并在提供匿名账户时完成受授权的账户合并。
	VerifyPhone(context.Context, string, string, string) (AuthResult, error)
	// Refresh 原子轮换活动登录会话并返回新凭证。
	Refresh(context.Context, string) (Tokens, error)
	// Logout 撤销 Refresh Token 所属登录会话。
	Logout(context.Context, string) error
	// Me 只返回可信认证上下文指定的账户，不接受客户端另传 account_id。
	Me(context.Context, string) (Account, error)
}
