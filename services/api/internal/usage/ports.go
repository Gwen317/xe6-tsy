package usage

import (
	"context"
	"time"
)

// Repository 提供用量事实的幂等持久化和只读汇总能力。
// 写入端必须区分“同键同 payload 的安全重放”和“同键不同 payload 的业务冲突”。
type Repository interface {
	// Record 至多保存一次事实，并通过 bool 告知调用方本次是否创建了新记录。
	Record(context.Context, RecordInput) (Detail, bool, error)
	// SessionSummary 汇总指定账户拥有的一场 Session 用量。
	SessionSummary(context.Context, string, string) (Summary, error)
	// AccountSummary 汇总账户在半开时间区间 [start, end) 内的用量。
	AccountSummary(context.Context, string, time.Time, time.Time) (Summary, error)
}

// SessionOwnerReader 由 Session 模块适配器实现，提供 Session 归属的权威读接口。
// 用量消费者用它拒绝 account_id 与 session_id 不一致的伪造或串线事实。
type SessionOwnerReader interface {
	// AccountIDForSession 返回 Session 创建时保存的权威账户归属。
	AccountIDForSession(context.Context, string) (string, error)
}

// CanonicalAccountResolver 允许在匿名账户合并后比较两个账户是否属于同一最终主体。
// 比较通过后仍保留事实创建时的原始 owner，避免重写历史用量归属。
type CanonicalAccountResolver interface {
	CanonicalAccountID(context.Context, string) (string, error)
}

// Service 定义消息消费者和 HTTP Handler 可以调用的用量业务用例。
type Service interface {
	// Record 校验事件版本、度量值和 Session 归属后执行幂等落库。
	Record(context.Context, RecordInput) (Detail, error)
	// SessionUsage 返回当前账户有权访问的一场 Session 汇总。
	SessionUsage(context.Context, string, string) (Summary, error)
	// AccountUsage 返回账户在已校验半开时间区间内的汇总。
	AccountUsage(context.Context, string, time.Time, time.Time) (Summary, error)
}
