package usage

import (
	"context"
	"math/big"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
)

// UseCases 是用量模块的业务编排层，负责校验事实、确认 Session 归属并调用幂等存储。
// 它不读取客户端声明的身份；HTTP 查询账户来自认证 Context，事件账户还要与 Session 权威归属核对。
type UseCases struct {
	repository Repository         // 用量明细与汇总的持久化端口。
	owners     SessionOwnerReader // Session 所有权及匿名账户合并链的权威读端口。
}

const (
	maxIdempotencyKeyLength = 200
	usageCostScale          = 8
	usageCostIntegerDigits  = 12
)

// 以下格式与 usage.recorded v1 契约保持一致。
// cost 与 currency 同时为空表示供应商没有报告价格，与明确报告 0 金额含义不同，PostgreSQL 会保存为 NULL。
// 精度限制在业务层提前校验，避免数据库对已经接受的金额执行静默舍入。
var (
	usageCostPattern     = regexp.MustCompile(`^(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)
	usageCurrencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
)

// NewUseCases 创建安全关闭实现；未接入 Repository 时返回 not_implemented，不在内存中伪造生产用量。
func NewUseCases() *UseCases { return &UseCases{} }

// NewPersistentUseCases 连接持久化 Repository 与 Session 权威归属读取器。
func NewPersistentUseCases(repository Repository, owners SessionOwnerReader) *UseCases {
	return &UseCases{repository: repository, owners: owners}
}

// Record 接收一条后端 usage.recorded 事实，校验契约和 Session 归属后执行幂等持久化。
// 若匿名账户已经合并，只允许 canonical account 相同的事实继续写入，并保留 Session 创建时的原始 owner。
func (u *UseCases) Record(ctx context.Context, input RecordInput) (Detail, error) {
	if u.repository == nil {
		return Detail{}, domain.ErrNotImplemented
	}
	if err := validate(input); err != nil {
		return Detail{}, err
	}
	if u.owners != nil {
		owner, err := u.owners.AccountIDForSession(ctx, input.SessionID)
		if err != nil {
			return Detail{}, err
		}
		if err := u.sameCanonicalAccount(ctx, owner, input.AccountID); err != nil {
			return Detail{}, err
		}
		// Session/account 复合外键保留 Session 创建时记录的不可变 owner。
		// 账户合并后的调用经 canonical 比较可以通过，但落库仍使用原始 owner，保证历史事实稳定。
		input.AccountID = owner
	}
	detail, _, err := u.repository.Record(ctx, input)
	return detail, err
}

// sameCanonicalAccount 判断两个账户 ID 是否最终归属于同一活动账户。
// 没有 resolver 时只允许 ID 完全相同；无法证明同源就返回 forbidden，而不是放宽归属校验。
func (u *UseCases) sameCanonicalAccount(ctx context.Context, left, right string) error {
	if left == right {
		return nil
	}
	resolver, ok := u.owners.(CanonicalAccountResolver)
	if !ok {
		return domain.ErrForbidden
	}
	canonicalLeft, err := resolver.CanonicalAccountID(ctx, left)
	if err != nil {
		return err
	}
	canonicalRight, err := resolver.CanonicalAccountID(ctx, right)
	if err != nil {
		return err
	}
	if canonicalLeft != canonicalRight {
		return domain.ErrForbidden
	}
	return nil
}

// SessionUsage 查询单场 Session 汇总前再次校验当前账户是否拥有该 Session。
// 这层校验位于业务层，消息消费者或其他内部调用也不能绕过 HTTP 中间件直接读取他人用量。
func (u *UseCases) SessionUsage(ctx context.Context, accountID, sessionID string) (Summary, error) {
	if u.repository == nil {
		return Summary{}, domain.ErrNotImplemented
	}
	if accountID == "" || sessionID == "" {
		return Summary{}, domain.ErrInvalidArgument
	}
	if u.owners != nil {
		owner, err := u.owners.AccountIDForSession(ctx, sessionID)
		if err != nil {
			return Summary{}, err
		}
		if err := u.sameCanonicalAccount(ctx, owner, accountID); err != nil {
			return Summary{}, err
		}
	}
	return u.repository.SessionSummary(ctx, accountID, sessionID)
}

// AccountUsage 返回账户在半开区间 [start, end) 内的汇总；非法或空时间范围会提前拒绝。
func (u *UseCases) AccountUsage(ctx context.Context, accountID string, start, end time.Time) (Summary, error) {
	if u.repository == nil {
		return Summary{}, domain.ErrNotImplemented
	}
	if accountID == "" || start.IsZero() || end.IsZero() || !start.Before(end) {
		return Summary{}, domain.ErrInvalidArgument
	}
	return u.repository.AccountSummary(ctx, accountID, start, end)
}

// validate 对 usage.recorded v1 做完整业务校验。
// 校验通过只代表结构和度量合法，Session 所有权与幂等冲突仍由 Record 后续步骤判断。
func validate(input RecordInput) error {
	if input.EventVersion != UsageEventVersion || input.ID == "" || input.TraceID == "" || input.IdempotencyKey == "" || input.AccountID == "" || input.SessionID == "" || input.TurnID == "" || input.Provider == "" || input.Model == "" || input.OccurredAt.IsZero() {
		return domain.ErrInvalidArgument
	}
	if !utf8.ValidString(input.IdempotencyKey) || utf8.RuneCountInString(input.IdempotencyKey) > maxIdempotencyKeyLength {
		return domain.ErrInvalidArgument
	}
	switch input.ServiceType {
	case StageASR, StageTranslation, StageTTS, StageDiarization:
	default:
		return domain.ErrInvalidArgument
	}
	if input.InputTokens < 0 || input.OutputTokens < 0 || input.AudioDurationMS < 0 {
		return domain.ErrInvalidArgument
	}
	// 金额与币种必须同时出现；只有一项会让汇总结果无法解释。
	if (input.CostAmount == "") != (input.Currency == "") {
		return domain.ErrInvalidArgument
	}
	if input.CostAmount != "" {
		if !usageCostPattern.MatchString(input.CostAmount) {
			return domain.ErrInvalidArgument
		}
		parts := strings.SplitN(input.CostAmount, ".", 2)
		if len(parts[0]) > usageCostIntegerDigits || (len(parts) == 2 && len(parts[1]) > usageCostScale) {
			return domain.ErrInvalidArgument
		}
		if _, ok := new(big.Rat).SetString(input.CostAmount); !ok {
			return domain.ErrInvalidArgument
		}
	}
	if input.Currency != "" && !usageCurrencyPattern.MatchString(input.Currency) {
		return domain.ErrInvalidArgument
	}
	return nil
}

// MemoryRepository 只用于确定性的本地单元测试，生产装配使用 PostgresRepository。
// 它与 PostgreSQL 实现共享 payload hash 规则，保证测试覆盖真实幂等语义。
type MemoryRepository struct {
	mu    sync.RWMutex
	facts []Detail
	byKey map[string]memoryRecord
}

type memoryRecord struct {
	detail      Detail
	payloadHash recordPayloadHash
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{byKey: make(map[string]memoryRecord)}
}
func (r *MemoryRepository) Record(ctx context.Context, input RecordInput) (Detail, bool, error) {
	if err := ctx.Err(); err != nil {
		return Detail{}, false, err
	}
	hash, err := hashRecordInput(input)
	if err != nil {
		return Detail{}, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.byKey[input.IdempotencyKey]; ok {
		// 相同 key 和相同 payload 是安全重放；相同 key 携带不同内容必须拒绝，不能覆盖原事实。
		if old.payloadHash != hash {
			return Detail{}, false, domain.ErrConflict
		}
		return old.detail, false, nil
	}
	detail := Detail{RecordInput: input, RecordedAt: time.Now().UTC()}
	r.byKey[input.IdempotencyKey] = memoryRecord{detail: detail, payloadHash: hash}
	r.facts = append(r.facts, detail)
	return detail, true, nil
}
func (r *MemoryRepository) SessionSummary(ctx context.Context, accountID, sessionID string) (Summary, error) {
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return summarize(r.facts, accountID, sessionID, time.Time{}, time.Time{})
}
func (r *MemoryRepository) AccountSummary(ctx context.Context, accountID string, start, end time.Time) (Summary, error) {
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return summarize(r.facts, accountID, "", start, end)
}
func summarize(facts []Detail, accountID, sessionID string, start, end time.Time) (Summary, error) {
	totals := map[Stage]*StageTotal{}
	for _, fact := range facts {
		if fact.AccountID != accountID || (sessionID != "" && fact.SessionID != sessionID) || (!start.IsZero() && (fact.OccurredAt.Before(start) || !fact.OccurredAt.Before(end))) {
			continue
		}
		if (fact.CostAmount == "") != (fact.Currency == "") {
			return Summary{}, domain.ErrConflict
		}
		total := totals[fact.ServiceType]
		if total == nil {
			total = &StageTotal{ServiceType: fact.ServiceType, Currency: fact.Currency}
			totals[fact.ServiceType] = total
		} else if total.Currency != fact.Currency {
			// 单个阶段汇总不能安全合并不同币种；该规则与 PostgreSQL 聚合实现保持一致。
			return Summary{}, domain.ErrConflict
		}
		total.InputTokens += fact.InputTokens
		total.OutputTokens += fact.OutputTokens
		total.AudioDurationMS += fact.AudioDurationMS
		if fact.CostAmount != "" {
			amount, ok := addMoney(total.CostAmount, fact.CostAmount)
			if !ok {
				return Summary{}, domain.ErrConflict
			}
			total.CostAmount = amount
		}
	}
	result := Summary{AccountID: accountID, SessionID: sessionID, PeriodStart: start, PeriodEnd: end, Totals: make([]StageTotal, 0)}
	for _, stage := range []Stage{StageASR, StageTranslation, StageTTS, StageDiarization} {
		if total := totals[stage]; total != nil {
			result.Totals = append(result.Totals, *total)
		}
	}
	return result, nil
}
func addMoney(left, right string) (string, bool) {
	// 使用有理数而不是 float64 累加金额，避免二进制浮点误差影响计费结果。
	if left == "" && right == "" {
		return "", true
	}
	if left == "" {
		left = "0"
	}
	if right == "" {
		right = "0"
	}
	l, leftOK := new(big.Rat).SetString(left)
	r, rightOK := new(big.Rat).SetString(right)
	if !leftOK || !rightOK {
		return "", false
	}
	l.Add(l, r)
	return l.FloatString(usageCostScale), true
}
