package usage

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresRepository 是用量事实与汇总的生产持久化实现。
// 明细写入采用幂等键加完整 payload hash，汇总读取同时覆盖账户匿名阶段到注册阶段的 lineage。
type PostgresRepository struct{ pool *pgxpool.Pool }

const usageDetailProjection = `event_version,event_id,trace_id,idempotency_key,payload_hash,account_id,session_id,turn_id,service_type,provider,model,input_tokens,output_tokens,audio_duration_ms,COALESCE(cost_amount::text,''),COALESCE(currency,''),occurred_at,recorded_at`

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

// Record 尝试插入一条不可变用量事实。
// 首次写入返回 created=true；同键同 payload 返回原记录和 created=false；同键不同 payload 返回 conflict。
func (r *PostgresRepository) Record(ctx context.Context, input RecordInput) (Detail, bool, error) {
	// 对完整事件计算 hash，而不是只对幂等键计算 hash。
	// 这样可以识别生产者错误复用同一个 key 但改变金额、Session 或 Provider 的情况。
	hash, err := hashRecordInput(input)
	if err != nil {
		return Detail{}, false, err
	}
	now := time.Now().UTC()
	var storedHash []byte
	// INSERT ... ON CONFLICT 是数据库层的并发闸门：同一个幂等键只有一个事实可以成功创建。
	// 使用 RETURNING 让首次写入和后续幂等重放都经过数据库的 NUMERIC/TIMESTAMPTZ 表示，
	// 避免第一次返回输入格式、重放却返回数据库格式造成响应不一致。
	row := r.pool.QueryRow(ctx, `INSERT INTO lingow_usage_records (event_version, event_id, trace_id, idempotency_key, payload_hash, account_id, session_id, turn_id, service_type, provider, model, input_tokens, output_tokens, audio_duration_ms, cost_amount, currency, occurred_at, recorded_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,NULLIF($15,'')::numeric,NULLIF($16,''),$17,$18) ON CONFLICT (idempotency_key) DO NOTHING RETURNING `+usageDetailProjection, input.EventVersion, input.ID, input.TraceID, input.IdempotencyKey, hash[:], input.AccountID, input.SessionID, input.TurnID, input.ServiceType, input.Provider, input.Model, input.InputTokens, input.OutputTokens, input.AudioDurationMS, input.CostAmount, input.Currency, input.OccurredAt.UTC(), now)
	detail, err := scanUsageDetail(row, &storedHash)
	if err == nil {
		return detail, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, false, mapUsageError(err)
	}

	// 没有 RETURNING 行说明幂等键已经存在。重新读取旧记录而不是重新构造输入，
	// 保证重放返回的 recorded_at、金额精度和数据库规范化结果与首次保存一致。
	detail, err = r.scanDetail(ctx, `SELECT `+usageDetailProjection+` FROM lingow_usage_records WHERE idempotency_key=$1`, &storedHash, input.IdempotencyKey)
	if err != nil {
		return Detail{}, false, err
	}
	if !equalHash(storedHash, hash[:]) {
		// 幂等键只能代表一个确定事实；payload 发生变化说明生产者错误复用了 key。
		return Detail{}, false, domain.ErrConflict
	}
	// 同 key 同 payload 是安全重放；created=false 让调用方可以观察重复投递，但不会重复计量。
	return detail, false, nil
}

func (r *PostgresRepository) SessionSummary(ctx context.Context, accountID, sessionID string) (Summary, error) {
	return r.summary(ctx, accountID, sessionID, time.Time{}, time.Time{})
}

func (r *PostgresRepository) AccountSummary(ctx context.Context, accountID string, start, end time.Time) (Summary, error) {
	return r.summary(ctx, accountID, "", start, end)
}

// summary 根据可选 Session 和半开时间区间聚合用量。
// lingow_account_lineage 会同时纳入账户匿名阶段和注册阶段的数据，而不修改历史事实 owner。
func (r *PostgresRepository) summary(ctx context.Context, accountID, sessionID string, start, end time.Time) (Summary, error) {
	// 查询使用账户 lineage，而不是只匹配当前 accountID，才能把匿名阶段产生的用量纳入正式账户汇总。
	// sessionID 和 [start, end) 是可选过滤条件，时间范围使用半开区间避免相邻查询重复计算边界记录。
	args := []any{accountID}
	where := `account_id IN (SELECT account_id FROM lingow_account_lineage($1))`
	if sessionID != "" {
		args = append(args, sessionID)
		where += fmt.Sprintf(" AND session_id=$%d", len(args))
	}
	if !start.IsZero() {
		args = append(args, start, end)
		where += fmt.Sprintf(" AND occurred_at >= $%d AND occurred_at < $%d", len(args)-1, len(args))
	}
	// 同时统计总行数和有价格行数，用于区分“该组价格全部未知”和“仅部分记录有价格”。
	// 后一种情况不能只累加已知价格并返回一个偏低总额，而必须报告冲突。
	rows, err := r.pool.Query(ctx, `SELECT service_type,COALESCE(currency,''),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),COALESCE(SUM(audio_duration_ms),0),COALESCE(SUM(cost_amount),0)::text,COUNT(*),COUNT(cost_amount) FROM lingow_usage_records WHERE `+where+` GROUP BY service_type,currency ORDER BY service_type,currency`, args...)
	if err != nil {
		return Summary{}, mapUsageError(err)
	}
	defer rows.Close()
	result := Summary{AccountID: accountID, SessionID: sessionID, PeriodStart: start, PeriodEnd: end, Totals: make([]StageTotal, 0)}
	seen := make(map[Stage]bool)
	for rows.Next() {
		var total StageTotal
		var rowCount, costCount int64
		if err := rows.Scan(&total.ServiceType, &total.Currency, &total.InputTokens, &total.OutputTokens, &total.AudioDurationMS, &total.CostAmount, &rowCount, &costCount); err != nil {
			return Summary{}, err
		}
		amount, err := aggregateCost(total.CostAmount, total.Currency, rowCount, costCount)
		if err != nil {
			return Summary{}, err
		}
		total.CostAmount = amount
		if seen[total.ServiceType] {
			// 同一阶段出现多条结果通常表示混入不同币种；当前契约无法安全合并，拒绝模糊汇总。
			return Summary{}, domain.ErrConflict
		}
		// SQL 已按 service_type、currency 分组；同一阶段再次出现结果说明混入多个币种，
		// 当前 Summary 契约无法安全表达这种情况，因此返回冲突而不是给出误导性总额。
		seen[total.ServiceType] = true
		result.Totals = append(result.Totals, total)
	}
	if err := rows.Err(); err != nil {
		return Summary{}, err
	}
	return result, nil
}

// aggregateCost 校验一组聚合结果是否具有完整且单一的定价语义。
// 全部未定价返回空金额；全部定价返回规范金额；部分定价或缺少币种均返回冲突。
func aggregateCost(amount, currency string, rowCount, costCount int64) (string, error) {
	if rowCount <= 0 || costCount < 0 || costCount > rowCount {
		return "", domain.ErrConflict
	}
	if costCount == 0 {
		if currency != "" {
			return "", domain.ErrConflict
		}
		return "", nil
	}
	if costCount != rowCount || currency == "" {
		return "", domain.ErrConflict
	}
	normalized, ok := addMoney("", amount)
	if !ok {
		return "", domain.ErrConflict
	}
	return normalized, nil
}

func (r *PostgresRepository) scanDetail(ctx context.Context, query string, hash *[]byte, args ...any) (Detail, error) {
	detail, err := scanUsageDetail(r.pool.QueryRow(ctx, query, args...), hash)
	return detail, mapUsageError(err)
}

func scanUsageDetail(row pgx.Row, hash *[]byte) (Detail, error) {
	var detail Detail
	var service Stage
	err := row.Scan(&detail.EventVersion, &detail.ID, &detail.TraceID, &detail.IdempotencyKey, hash, &detail.AccountID, &detail.SessionID, &detail.TurnID, &service, &detail.Provider, &detail.Model, &detail.InputTokens, &detail.OutputTokens, &detail.AudioDurationMS, &detail.CostAmount, &detail.Currency, &detail.OccurredAt, &detail.RecordedAt)
	detail.ServiceType = service
	return detail, err
}

func equalHash(left, right []byte) bool {
	// 摘要属于幂等判断的一部分，使用常量时间比较避免内容相关的比较时序差异。
	return len(left) == len(right) && subtle.ConstantTimeCompare(left, right) == 1
}

// mapUsageError 将数据库错误稳定映射为领域错误，避免 HTTP 或消息消费者依赖 PostgreSQL 错误码。
func mapUsageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return domain.ErrConflict
		case "23503":
			return domain.ErrNotFound
		case "23514", "22003", "22P02":
			return domain.ErrInvalidArgument
		}
	}
	return fmt.Errorf("postgres usage operation: %w", err)
}
