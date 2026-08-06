package accounts

import "time"

// AccountKind 区分临时匿名身份与已经完成登录验证的注册身份。
// 两种身份都可以拥有业务数据；手机号登录时只迁移归属关系，不重写既有业务记录。
type AccountKind string

const (
	// AccountKindAnonymous 表示尚未绑定已验证登录身份的临时账户。
	AccountKindAnonymous AccountKind = "anonymous"
	// AccountKindRegistered 表示已经通过手机号等登录流程确认身份的正式账户。
	AccountKindRegistered AccountKind = "registered"
)

// Account 是会话、用量、记录和消息投递共同引用的稳定归属主体。
// 客户端不能自行声明 Account.ID；HTTP 层只能使用 Access Token 校验后得到的账户 ID。
type Account struct {
	ID        string      `json:"id"`         // 服务端生成的账户唯一标识。
	Kind      AccountKind `json:"kind"`       // 当前是匿名账户还是已验证注册账户。
	CreatedAt time.Time   `json:"created_at"` // 账户首次创建时间，不随匿名账户合并而改变。
}

// Session 表示一条可撤销的登录会话。
// 数据库只保存 Refresh Token 的摘要；刷新或退出时先用摘要定位会话，再执行轮换或撤销。
type Session struct {
	ID          string     // 登录会话 ID，同时写入 Access Token 的 sid 声明。
	AccountID   string     // 登录会话当前所属账户；匿名账户合并后会迁移到注册账户。
	RefreshHash string     // Refresh Token 的不可逆摘要，避免数据库泄漏明文凭证。
	ExpiresAt   time.Time  // 登录会话到期时间，过期后不能刷新或继续授权。
	RevokedAt   *time.Time // 撤销时间；非空表示刷新链或退出操作已经使会话失效。
	CreatedAt   time.Time  // 登录会话创建时间。
}

// PhoneChallenge 保存一次手机号验证码登录所需的非明文状态。
// 验证码、手机号均以摘要或加密兼容值保存，并通过过期时间、尝试次数和一次性消费限制重放。
type PhoneChallenge struct {
	ID                  string     // 客户端后续提交的挑战 ID，本身不包含手机号信息。
	PhoneHash           string     // 使用认证专用 pepper 计算的当前版本手机号摘要。
	LegacyPhoneHash     string     // 为历史摘要迁移临时保存的加密查询值。
	LegacyRateLimitHash string     // 仅用于兼容旧数据的限流键，新挑战不会持久化该明文兼容值。
	CodeHash            string     // 绑定 challenge ID 的验证码摘要，不能跨挑战复用。
	DigestVersion       int16      // 摘要算法版本，用于兼容数据迁移。
	ExpiresAt           time.Time  // 挑战过期时间。
	UsedAt              *time.Time // 一次性消费时间；非空表示成功验证码已被使用。
	CreatedAt           time.Time  // 挑战创建时间，也是发送频率限制的统计依据。
	Attempts            int16      // 已失败的验证次数，错误验证码也必须持久化计数。
	MaxAttempts         int16      // 允许的最大尝试次数。
	LastAttemptAt       *time.Time // 最近一次验证时间，便于审计和限流判断。
}

// Tokens 是匿名认证、手机号登录或刷新成功后返回的凭证对。
// Access Token 用于访问受保护接口；Refresh Token 只用于轮换新的凭证，不作为业务身份直接使用。
type Tokens struct {
	AccessToken  string    `json:"access_token"`  // 短期 JWT，携带可信 account_id 与 session_id。
	RefreshToken string    `json:"refresh_token"` // 高熵不透明凭证，服务端仅保存其摘要。
	ExpiresAt    time.Time `json:"expires_at"`    // Access Token 的过期时间。
}

// AuthResult 将认证后的账户和新签发的凭证作为一个原子结果返回客户端。
type AuthResult struct {
	Account Account `json:"account"`
	Tokens  Tokens  `json:"tokens"`
}

// AccessTokenClaims 只保存经过 Access Token 签名、有效期和会话状态校验后的可信身份。
// HTTP 适配层禁止使用请求体、查询参数或自定义 Header 中的 account_id 构造该对象。
type AccessTokenClaims struct {
	AccountID string // Token 的 sub，表示请求所属账户。
	SessionID string // Token 的 sid，用于检查登录会话是否仍然有效且仍属于该账户。
}
