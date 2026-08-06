package accounts

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
	"github.com/oklog/ulid/v2"
)

// PostgresRepository 持有账户、验证码挑战和登录会话的权威持久化状态。
// 数据库只保存凭证摘要；Access Token 校验同时依赖签名和下方的活动会话查询。
type PostgresRepository struct{ pool *pgxpool.Pool }

const insertRegisteredAccountSQL = `INSERT INTO lingow_accounts (id, kind, phone_hash_v2, created_at) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`
const insertSessionSQL = `INSERT INTO lingow_auth_sessions (id, account_id, refresh_hash, expires_at, created_at) VALUES ($1,$2,$3,$4,$5)`
const revokeActiveSessionSQL = `UPDATE lingow_auth_sessions SET revoked_at=CURRENT_TIMESTAMP WHERE id=$1 AND revoked_at IS NULL`
const rotateActiveSessionSQL = `UPDATE lingow_auth_sessions SET revoked_at=CURRENT_TIMESTAMP WHERE id=$1 AND account_id=$2 AND revoked_at IS NULL AND expires_at > CURRENT_TIMESTAMP`
const challengeAdvisoryLockSQL = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`
const challengeRateQuerySQL = `
	SELECT MAX(created_at), COUNT(*) FILTER (WHERE created_at > $2)
	FROM lingow_phone_challenges
	WHERE phone_hash = $1
	   OR (digest_version = 1 AND phone_hash = $3)`

const (
	phoneChallengeCooldown                 = time.Minute
	phoneChallengeWindow                   = time.Hour
	phoneChallengeWindowMaxSends           = 5
	defaultPhoneChallengeMaxAttempts int16 = 5
)

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func (r *PostgresRepository) CreateAnonymous(ctx context.Context) (Account, error) {
	now := time.Now().UTC()
	account := Account{ID: "acct_" + ulid.Make().String(), Kind: AccountKindAnonymous, CreatedAt: now}
	_, err := r.pool.Exec(ctx, `INSERT INTO lingow_accounts (id, kind, created_at) VALUES ($1,$2,$3)`, account.ID, string(account.Kind), now)
	return account, mapError(err)
}

func (r *PostgresRepository) GetAccount(ctx context.Context, id string) (Account, error) {
	var account Account
	var kind string
	err := r.pool.QueryRow(ctx, `SELECT id, kind, created_at FROM lingow_accounts WHERE id=$1 AND merged_into IS NULL`, id).Scan(&account.ID, &kind, &account.CreatedAt)
	if err != nil {
		return Account{}, mapError(err)
	}
	account.Kind = AccountKind(kind)
	return account, nil
}

// CreateChallenge 在事务内串行化同一手机号的并发申请，执行冷却和小时级发送上限后再写入挑战。
// 即使多个 API 实例同时处理同一手机号，也只能基于同一组权威计数做出决定。
func (r *PostgresRepository) CreateChallenge(ctx context.Context, challenge PhoneChallenge) error {
	if challenge.MaxAttempts == 0 {
		challenge.MaxAttempts = defaultPhoneChallengeMaxAttempts
	}
	if challenge.CreatedAt.IsZero() {
		challenge.CreatedAt = time.Now().UTC()
	}
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		// advisory lock 按私有手机号摘要串行化跨实例请求，即使该手机号还没有任何挑战记录也有效。
		if _, err := tx.Exec(ctx, challengeAdvisoryLockSQL, challenge.PhoneHash); err != nil {
			return err
		}

		var latest *time.Time
		var sends int64
		if err := tx.QueryRow(ctx, challengeRateQuerySQL,
			challenge.PhoneHash, challenge.CreatedAt.Add(-phoneChallengeWindow), challenge.LegacyRateLimitHash,
		).Scan(&latest, &sends); err != nil {
			return err
		}
		if latest != nil && challenge.CreatedAt.Before(latest.Add(phoneChallengeCooldown)) {
			return domain.ErrRateLimited
		}
		if sends >= phoneChallengeWindowMaxSends {
			return domain.ErrRateLimited
		}

		_, err := tx.Exec(ctx, `
			INSERT INTO lingow_phone_challenges
				(id, phone_hash, legacy_phone_hash, code_hash, digest_version, expires_at, created_at, attempts, max_attempts)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			challenge.ID, challenge.PhoneHash, challenge.LegacyPhoneHash, challenge.CodeHash, challenge.DigestVersion,
			challenge.ExpiresAt, challenge.CreatedAt, challenge.Attempts, challenge.MaxAttempts)
		return err
	})
	return mapError(err)
}

// ConsumeChallenge 对挑战行加锁，原子检查过期、消费状态、尝试上限和验证码摘要。
// 错误验证码先增加 attempts 并提交事务，再返回 unauthorized；正确验证码写入 used_at 后返回私有绑定信息。
func (r *PostgresRepository) ConsumeChallenge(ctx context.Context, id, code string) (PhoneChallenge, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return PhoneChallenge{}, err
	}
	defer tx.Rollback(ctx)

	var challenge PhoneChallenge
	err = tx.QueryRow(ctx, `
		SELECT id, phone_hash, legacy_phone_hash, code_hash, digest_version, expires_at, used_at, created_at,
			attempts, max_attempts, last_attempt_at
		FROM lingow_phone_challenges
		WHERE id = $1
		FOR UPDATE`, id).Scan(
		&challenge.ID, &challenge.PhoneHash, &challenge.LegacyPhoneHash, &challenge.CodeHash, &challenge.DigestVersion, &challenge.ExpiresAt,
		&challenge.UsedAt, &challenge.CreatedAt, &challenge.Attempts,
		&challenge.MaxAttempts, &challenge.LastAttemptAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PhoneChallenge{}, domain.ErrUnauthorized
		}
		return PhoneChallenge{}, mapError(err)
	}

	now := time.Now().UTC()
	if challenge.UsedAt != nil || !now.Before(challenge.ExpiresAt) || challenge.Attempts >= challenge.MaxAttempts {
		return PhoneChallenge{}, domain.ErrUnauthorized
	}
	if challenge.DigestVersion != 2 || subtle.ConstantTimeCompare([]byte(code), []byte(challenge.CodeHash)) != 1 {
		// 该错误分支必须先提交 attempts 再返回；如果事务回滚，攻击者可以无限猜测同一个挑战。
		if _, err := tx.Exec(ctx, `
			UPDATE lingow_phone_challenges
			SET attempts = attempts + 1, last_attempt_at = $2
			WHERE id = $1`, id, now); err != nil {
			return PhoneChallenge{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return PhoneChallenge{}, err
		}
		return PhoneChallenge{}, domain.ErrUnauthorized
	}

	if _, err := tx.Exec(ctx, `
		UPDATE lingow_phone_challenges
		SET used_at = $2, last_attempt_at = $2
		WHERE id = $1`, id, now); err != nil {
		return PhoneChallenge{}, err
	}
	challenge.UsedAt = &now
	challenge.LastAttemptAt = &now
	if err := tx.Commit(ctx); err != nil {
		return PhoneChallenge{}, err
	}
	return challenge, nil
}

func (r *PostgresRepository) RestoreChallenge(ctx context.Context, id string) error {
	// 已成功消费的行不会被其他验证流程再次领取；只清理 used_at 非空的记录，
	// 可以补偿后续持久化失败，又不会把错误验证码尝试变成可重用挑战。
	_, err := r.pool.Exec(ctx, `UPDATE lingow_phone_challenges SET used_at=NULL WHERE id=$1 AND used_at IS NOT NULL`, id)
	return mapError(err)
}

func (r *PostgresRepository) FindOrCreateByPhoneHashes(ctx context.Context, phoneHash, legacyPhoneHash string) (Account, error) {
	var account Account
	var kind string
	err := r.pool.QueryRow(ctx, `SELECT id, kind, created_at FROM lingow_accounts WHERE phone_hash_v2=$1 AND merged_into IS NULL`, phoneHash).Scan(&account.ID, &kind, &account.CreatedAt)
	if err == nil {
		account.Kind = AccountKind(kind)
		return account, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Account{}, mapError(err)
	}
	// 旧账户通过历史 SHA-256 摘要定位并原地升级；写入带密钥的 v2 摘要后删除旧值，
	// 使弱确定性标识只保留到该账户第一次完成新版本登录。
	err = r.pool.QueryRow(ctx, `SELECT id, kind, created_at FROM lingow_accounts WHERE phone_hash=$1 AND merged_into IS NULL`, legacyPhoneHash).Scan(&account.ID, &kind, &account.CreatedAt)
	if err == nil {
		account.Kind = AccountKind(kind)
		if _, err := r.pool.Exec(ctx, `UPDATE lingow_accounts SET phone_hash_v2=$2, phone_hash=NULL WHERE id=$1 AND phone_hash_v2 IS NULL`, account.ID, phoneHash); err != nil {
			return Account{}, mapError(err)
		}
		return account, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Account{}, mapError(err)
	}
	account = Account{ID: "acct_" + ulid.Make().String(), Kind: AccountKindRegistered, CreatedAt: time.Now().UTC()}
	// 新账户不再持久化弱确定性 SHA-256 值；legacy hash 只用于上方查找和升级旧账户。
	_, err = r.pool.Exec(ctx, insertRegisteredAccountSQL, account.ID, string(account.Kind), phoneHash, account.CreatedAt)
	if err != nil {
		return Account{}, mapError(err)
	}
	return r.GetAccountByPhoneHashesOrID(ctx, phoneHash, legacyPhoneHash, account.ID)
}

func (r *PostgresRepository) GetAccountByPhoneHashesOrID(ctx context.Context, phoneHash, legacyPhoneHash, fallbackID string) (Account, error) {
	var account Account
	var kind string
	err := r.pool.QueryRow(ctx, `SELECT id, kind, created_at FROM lingow_accounts WHERE (phone_hash_v2=$1 OR phone_hash=$2 OR id=$3) AND merged_into IS NULL ORDER BY CASE WHEN phone_hash_v2=$1 THEN 0 WHEN phone_hash=$2 THEN 1 ELSE 2 END LIMIT 1`, phoneHash, legacyPhoneHash, fallbackID).Scan(&account.ID, &kind, &account.CreatedAt)
	if err != nil {
		return Account{}, mapError(err)
	}
	account.Kind = AccountKind(kind)
	return account, nil
}

func (r *PostgresRepository) BindAnonymous(ctx context.Context, anonymousID, registeredID string) (Account, error) {
	if anonymousID == "" || registeredID == "" || anonymousID == registeredID {
		return Account{}, domain.ErrConflict
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback(ctx)
	var anonymousKind string
	var mergedInto *string
	if err := tx.QueryRow(ctx, `SELECT kind, merged_into FROM lingow_accounts WHERE id=$1 FOR UPDATE`, anonymousID).Scan(&anonymousKind, &mergedInto); err != nil {
		return Account{}, mapError(err)
	}
	if mergedInto != nil {
		if *mergedInto == registeredID {
			if err := tx.Commit(ctx); err != nil {
				return Account{}, mapError(err)
			}
			return r.GetAccount(ctx, registeredID)
		}
		return Account{}, domain.ErrConflict
	}
	if anonymousKind != string(AccountKindAnonymous) {
		return Account{}, domain.ErrConflict
	}
	var registeredKind string
	if err := tx.QueryRow(ctx, `SELECT kind FROM lingow_accounts WHERE id=$1 AND merged_into IS NULL FOR UPDATE`, registeredID).Scan(&registeredKind); err != nil {
		return Account{}, mapError(err)
	}
	if registeredKind != string(AccountKindRegistered) {
		return Account{}, domain.ErrConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE lingow_auth_sessions SET account_id=$2 WHERE account_id=$1`, anonymousID, registeredID); err != nil {
		return Account{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE lingow_accounts SET merged_into=$2 WHERE id=$1`, anonymousID, registeredID); err != nil {
		return Account{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Account{}, err
	}
	return r.GetAccount(ctx, registeredID)
}

// BindAnonymousAndCreateSession 是手机号登录主路径使用的匿名账户合并操作。
// 账户归属迁移和首个正式登录会话在同一事务提交；会话插入失败会回滚全部合并变化。
func (r *PostgresRepository) BindAnonymousAndCreateSession(ctx context.Context, anonymousID, registeredID string, session Session) (Account, error) {
	if anonymousID == "" || registeredID == "" || anonymousID == registeredID || session.ID == "" || session.AccountID != registeredID {
		return Account{}, domain.ErrConflict
	}
	var account Account
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var anonymousKind string
		var mergedInto *string
		if err := tx.QueryRow(ctx, `SELECT kind, merged_into FROM lingow_accounts WHERE id=$1 FOR UPDATE`, anonymousID).Scan(&anonymousKind, &mergedInto); err != nil {
			return mapError(err)
		}
		if mergedInto != nil && *mergedInto != registeredID {
			return domain.ErrConflict
		}
		if mergedInto == nil && anonymousKind != string(AccountKindAnonymous) {
			return domain.ErrConflict
		}
		var registeredKind string
		var createdAt time.Time
		if err := tx.QueryRow(ctx, `SELECT kind, created_at FROM lingow_accounts WHERE id=$1 AND merged_into IS NULL FOR UPDATE`, registeredID).Scan(&registeredKind, &createdAt); err != nil {
			return mapError(err)
		}
		if registeredKind != string(AccountKindRegistered) {
			return domain.ErrConflict
		}
		if mergedInto == nil {
			if _, err := tx.Exec(ctx, `UPDATE lingow_auth_sessions SET account_id=$2 WHERE account_id=$1`, anonymousID, registeredID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE lingow_accounts SET merged_into=$2 WHERE id=$1`, anonymousID, registeredID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, insertSessionSQL, session.ID, session.AccountID, session.RefreshHash, session.ExpiresAt, session.CreatedAt); err != nil {
			return err
		}
		account = Account{ID: registeredID, Kind: AccountKindRegistered, CreatedAt: createdAt}
		return nil
	})
	if err != nil {
		return Account{}, mapError(err)
	}
	return account, nil
}

func (r *PostgresRepository) CreateSession(ctx context.Context, session Session) error {
	_, err := r.pool.Exec(ctx, insertSessionSQL, session.ID, session.AccountID, session.RefreshHash, session.ExpiresAt, session.CreatedAt)
	return mapError(err)
}

func (r *PostgresRepository) GetSessionByRefreshHash(ctx context.Context, hash string) (Session, error) {
	var session Session
	err := r.pool.QueryRow(ctx, `SELECT id, account_id, refresh_hash, expires_at, revoked_at, created_at FROM lingow_auth_sessions WHERE refresh_hash=$1 AND revoked_at IS NULL AND expires_at > CURRENT_TIMESTAMP`, hash).Scan(&session.ID, &session.AccountID, &session.RefreshHash, &session.ExpiresAt, &session.RevokedAt, &session.CreatedAt)
	return session, mapError(err)
}

func (r *PostgresRepository) RotateSession(ctx context.Context, currentSessionID string, successor Session) error {
	// 撤销旧会话与插入后继会话共用一个事务，插入失败时两项变化一起回滚。
	// 条件 UPDATE 也会串行化并发刷新：只有真正把活动行改为 revoked 的事务可以创建后继会话。
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, rotateActiveSessionSQL, currentSessionID, successor.AccountID)
		if err != nil {
			return err
		}
		if err := revokeSessionResult(result.RowsAffected()); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, insertSessionSQL, successor.ID, successor.AccountID, successor.RefreshHash, successor.ExpiresAt, successor.CreatedAt)
		return err
	})
	return mapError(err)
}

func (r *PostgresRepository) RevokeSession(ctx context.Context, id string) error {
	// 条件撤销会把过期退出请求或重放显式映射为无效凭证，而不是假装已撤销会话仍处于活动状态。
	result, err := r.pool.Exec(ctx, revokeActiveSessionSQL, id)
	if err != nil {
		return mapError(err)
	}
	return revokeSessionResult(result.RowsAffected())
}

func revokeSessionResult(rowsAffected int64) error {
	if rowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// PurgeExpiredAuthSessions 批量撤销已过期登录会话，控制活动查询范围并防止过期凭证继续显示为活动。
func (r *PostgresRepository) PurgeExpiredAuthSessions(ctx context.Context) (int64, error) {
	result, err := r.pool.Exec(ctx, `
		UPDATE lingow_auth_sessions
		SET revoked_at = CURRENT_TIMESTAMP
		WHERE revoked_at IS NULL
		  AND expires_at <= CURRENT_TIMESTAMP`)
	if err != nil {
		return 0, mapError(err)
	}
	return result.RowsAffected(), nil
}

// PurgeStalePhoneChallenges 删除过期或已消费超过保留期的挑战，避免验证码状态无限增长。
func (r *PostgresRepository) PurgeStalePhoneChallenges(ctx context.Context, retention time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-retention)
	result, err := r.pool.Exec(ctx, `
		DELETE FROM lingow_phone_challenges
		WHERE expires_at <= CURRENT_TIMESTAMP
		   OR (used_at IS NOT NULL AND used_at <= $1)`, cutoff)
	if err != nil {
		return 0, mapError(err)
	}
	return result.RowsAffected(), nil
}

func (r *PostgresRepository) SessionActive(ctx context.Context, id string) (bool, error) {
	var active bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lingow_auth_sessions WHERE id=$1 AND revoked_at IS NULL AND expires_at > CURRENT_TIMESTAMP)`, id).Scan(&active)
	return active, mapError(err)
}

// SessionActiveForAccount 同时校验登录会话生命周期与当前账户归属。
// 匿名账户合并会迁移既有会话；加入 account 条件后，合并前签发的旧 subject Token 会立即失效。
func (r *PostgresRepository) SessionActiveForAccount(ctx context.Context, sessionID, accountID string) (bool, error) {
	var active bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lingow_auth_sessions WHERE id=$1 AND account_id=$2 AND revoked_at IS NULL AND expires_at > CURRENT_TIMESTAMP)`, sessionID, accountID).Scan(&active)
	return active, mapError(err)
}

// AccountIDForSession 返回业务 Session 创建时保存的不可变账户 owner，供用量模块校验事件归属。
func (r *PostgresRepository) AccountIDForSession(ctx context.Context, sessionID string) (string, error) {
	var accountID string
	err := r.pool.QueryRow(ctx, `SELECT account_id FROM voice_sessions WHERE id=$1`, sessionID).Scan(&accountID)
	return accountID, mapError(err)
}

// CanonicalAccountID 沿账户合并链查找当前活动账户。
// 递归查询携带 visited 集合，避免异常历史数据形成环后无限递归。
func (r *PostgresRepository) CanonicalAccountID(ctx context.Context, accountID string) (string, error) {
	var canonicalID string
	err := r.pool.QueryRow(ctx, `
		WITH RECURSIVE ancestors AS (
			SELECT id, merged_into, ARRAY[id] AS visited
			FROM lingow_accounts
			WHERE id = $1
			UNION ALL
			SELECT parent.id, parent.merged_into, child.visited || parent.id
			FROM lingow_accounts AS parent
			JOIN ancestors AS child ON parent.id = child.merged_into
			WHERE NOT parent.id = ANY(child.visited)
		)
		SELECT id FROM ancestors
		WHERE merged_into IS NULL
		LIMIT 1`, accountID).Scan(&canonicalID)
	return canonicalID, mapError(err)
}

func mapError(err error) error {
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
		}
	}
	return fmt.Errorf("postgres account operation: %w", err)
}
