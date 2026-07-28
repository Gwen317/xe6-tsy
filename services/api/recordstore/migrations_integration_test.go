//go:build integration

package recordstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrateRecordsSchema(t *testing.T) {
	pool := testDatabase(t)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatalf("first Migrate() error = %v", err)
	}
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}

	statuses, err := AppliedMigrations(t.Context(), pool)
	if err != nil {
		t.Fatalf("AppliedMigrations() error = %v", err)
	}
	if len(statuses) != 8 {
		t.Fatalf("len(AppliedMigrations()) = %d, want 8", len(statuses))
	}
	if status := statuses[0]; status.Version != 1 || status.Name != "voice_records" || status.AppliedAt.IsZero() {
		t.Fatalf("AppliedMigrations()[0] = %#v, want applied voice_records version 1", status)
	}
	if status := statuses[1]; status.Version != 2 || status.Name != "member5_control_plane" || status.AppliedAt.IsZero() {
		t.Fatalf("AppliedMigrations()[1] = %#v, want applied member5_control_plane version 2", status)
	}
	if status := statuses[2]; status.Version != 3 || status.Name != "session_operation_compat" || status.AppliedAt.IsZero() {
		t.Fatalf("AppliedMigrations()[2] = %#v, want applied session_operation_compat version 3", status)
	}
	if status := statuses[3]; status.Version != 4 || status.Name != "phone_challenge_hardening" || status.AppliedAt.IsZero() {
		t.Fatalf("AppliedMigrations()[3] = %#v, want applied phone_challenge_hardening version 4", status)
	}
	if status := statuses[4]; status.Version != 5 || status.Name != "account_lineage" || status.AppliedAt.IsZero() {
		t.Fatalf("AppliedMigrations()[4] = %#v, want applied account_lineage version 5", status)
	}
	if status := statuses[5]; status.Version != 6 || status.Name != "phone_digest_v2" || status.AppliedAt.IsZero() {
		t.Fatalf("AppliedMigrations()[5] = %#v, want applied phone_digest_v2 version 6", status)
	}
	if status := statuses[6]; status.Version != 7 || status.Name != "delivery_wecom_bot" || status.AppliedAt.IsZero() {
		t.Fatalf("AppliedMigrations()[6] = %#v, want applied delivery_wecom_bot version 7", status)
	}
	if status := statuses[7]; status.Version != 8 || status.Name != "phone_digest_cleanup" || status.AppliedAt.IsZero() {
		t.Fatalf("AppliedMigrations()[7] = %#v, want applied phone_digest_cleanup version 8", status)
	}
}

func TestMigration4AddsPhoneChallengeAttemptState(t *testing.T) {
	pool := testDatabase(t)
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatalf("embeddedMigrations() error = %v", err)
	}
	applyMigrationsThrough(t, pool, migrations, 3)

	createdAt := time.Date(2026, time.July, 28, 9, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO lingow_phone_challenges (id, phone_hash, code_hash, expires_at, created_at)
		VALUES ('challenge_legacy', 'phone_hash_legacy', 'code_hash_legacy', $1, $2)`, createdAt.Add(time.Hour), createdAt); err != nil {
		t.Fatalf("insert legacy phone challenge: %v", err)
	}
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatalf("Migrate() from version 3 error = %v", err)
	}

	var attempts, maxAttempts int16
	if err := pool.QueryRow(t.Context(), `
		SELECT attempts, max_attempts
		FROM lingow_phone_challenges WHERE id = 'challenge_legacy'`).Scan(&attempts, &maxAttempts); err != nil {
		t.Fatalf("read upgraded phone challenge: %v", err)
	}
	if attempts != 0 || maxAttempts != 5 {
		t.Fatalf("upgraded phone challenge counters = (%d, %d), want (0, 5)", attempts, maxAttempts)
	}

	_, err = pool.Exec(t.Context(), `
		UPDATE lingow_phone_challenges SET attempts = max_attempts + 1 WHERE id = 'challenge_legacy'`)
	assertPostgresCode(t, err, "23514")
}

func TestMigration3UpgradesDeployedControlPlaneSchema(t *testing.T) {
	pool := testDatabase(t)
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatalf("embeddedMigrations() error = %v", err)
	}
	applyMigrationsThrough(t, pool, migrations, 2)

	createdAt := time.Date(2026, time.July, 28, 9, 0, 0, 0, time.UTC)
	startedAt := createdAt.Add(time.Minute)
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO lingow_accounts (id, kind, created_at)
		VALUES ('acct_legacy', 'anonymous', $1)`, createdAt); err != nil {
		t.Fatalf("insert legacy account: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO voice_sessions (
			id, account_id, status, audio_config, capabilities, started_at, created_at
		) VALUES (
			'session_legacy', 'acct_legacy', 'active', '{}'::jsonb, '{}'::jsonb, $1, $2
		)`, startedAt, createdAt); err != nil {
		t.Fatalf("insert legacy active session: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO voice_session_start_requests (
			session_id, account_id, idempotency_key, request_hash, started_at, created_at
		) VALUES (
			'session_legacy', 'acct_legacy', 'start_legacy', 'hash_legacy', $1, $2
		)`, startedAt, createdAt); err != nil {
		t.Fatalf("insert legacy start request: %v", err)
	}

	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatalf("Migrate() from version 2 error = %v", err)
	}

	var operationID string
	var status string
	var compensationClaimID *string
	var preservedStartedAt time.Time
	var updatedAt time.Time
	err = pool.QueryRow(t.Context(), `
		SELECT operation_id, status, compensation_claim_id, started_at, updated_at
		FROM voice_session_start_requests
		WHERE account_id = 'acct_legacy' AND idempotency_key = 'start_legacy'`,
	).Scan(&operationID, &status, &compensationClaimID, &preservedStartedAt, &updatedAt)
	if err != nil {
		t.Fatalf("read upgraded start operation: %v", err)
	}
	if operationID != "legacy:start_legacy" || status != "completed" || compensationClaimID != nil {
		t.Fatalf("upgraded start operation = (%q, %q, %v), want completed legacy operation", operationID, status, compensationClaimID)
	}
	if !preservedStartedAt.Equal(startedAt) || !updatedAt.Equal(startedAt) {
		t.Fatalf("upgraded timestamps = (%v, %v), want preserved start %v", preservedStartedAt, updatedAt, startedAt)
	}
}

func TestRecordSchemaConstraints(t *testing.T) {
	pool := testDatabase(t)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	testConcurrentSpeakerMappingConstraint(t, pool)
	testProviderSpeakerConstraint(t, pool)
	testTurnConstraints(t, pool)
	testSessionLifecycleConstraints(t, pool)
	testStartOperationConstraints(t, pool)
}

func applyMigrationsThrough(t *testing.T, pool *pgxpool.Pool, migrations []migration, version int64) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `
		CREATE TABLE recordstore_schema_migrations (
			version BIGINT PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		t.Fatalf("create migration state: %v", err)
	}
	for _, migration := range migrations {
		if migration.Version > version {
			break
		}
		if _, err := pool.Exec(t.Context(), migration.SQL, pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatalf("apply migration %d for upgrade fixture: %v", migration.Version, err)
		}
		if _, err := pool.Exec(t.Context(),
			"INSERT INTO recordstore_schema_migrations (version, name) VALUES ($1, $2)",
			migration.Version,
			migration.Name,
		); err != nil {
			t.Fatalf("record migration %d for upgrade fixture: %v", migration.Version, err)
		}
	}
}

func testSessionLifecycleConstraints(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	createdAt := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	endedAt := createdAt.Add(time.Minute)
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO lingow_accounts (id, kind, created_at)
		VALUES ('acct_session_constraints', 'anonymous', $1)`, createdAt); err != nil {
		t.Fatalf("insert session constraint account: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO voice_sessions (
			id, account_id, status, audio_config, capabilities, ended_at, created_at
		) VALUES (
			'session_ended_before_start', 'acct_session_constraints', 'ended',
			'{}'::jsonb, '{}'::jsonb, $1, $2
		)`, endedAt, createdAt); err != nil {
		t.Fatalf("insert directly-ended session: %v", err)
	}
	_, err := pool.Exec(t.Context(), `
		INSERT INTO voice_sessions (
			id, account_id, status, audio_config, capabilities, ended_at, created_at
		) VALUES (
			'session_invalid_end_time', 'acct_session_constraints', 'ended',
			'{}'::jsonb, '{}'::jsonb, $1, $2
		)`, createdAt.Add(-time.Second), createdAt)
	assertPostgresCode(t, err, "23514")
}

func testStartOperationConstraints(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	createdAt := time.Date(2026, time.July, 28, 11, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO lingow_accounts (id, kind, created_at)
		VALUES ('acct_start_constraints', 'anonymous', $1)`, createdAt); err != nil {
		t.Fatalf("insert start-operation constraint account: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO voice_sessions (
			id, account_id, status, audio_config, capabilities, created_at
		) VALUES (
			'session_start_constraints', 'acct_start_constraints', 'created',
			'{}'::jsonb, '{}'::jsonb, $1
		)`, createdAt); err != nil {
		t.Fatalf("insert start-operation constraint session: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO voice_session_start_requests (
			operation_id, session_id, account_id, idempotency_key, request_hash,
			status, created_at, updated_at
		) VALUES (
			'op_pending', 'session_start_constraints', 'acct_start_constraints',
			'start_pending', 'hash_pending', 'pending', $1, $1
		)`, createdAt); err != nil {
		t.Fatalf("insert pending start operation: %v", err)
	}
	_, err := pool.Exec(t.Context(), `
		INSERT INTO voice_session_start_requests (
			operation_id, session_id, account_id, idempotency_key, request_hash,
			status, started_at, created_at, updated_at
		) VALUES (
			'op_invalid_pending', 'session_start_constraints', 'acct_start_constraints',
			'start_invalid_pending', 'hash_invalid_pending', 'pending', $1, $1, $1
		)`, createdAt)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(t.Context(), `
		INSERT INTO voice_session_start_requests (
			operation_id, session_id, account_id, idempotency_key, request_hash,
			status, created_at, updated_at
		) VALUES (
			'op_invalid_compensating', 'session_start_constraints', 'acct_start_constraints',
			'start_invalid_compensating', 'hash_invalid_compensating', 'compensating', $1, $1
		)`, createdAt)
	assertPostgresCode(t, err, "23514")
}

func testConcurrentSpeakerMappingConstraint(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	const writers = 8
	start := make(chan struct{})
	errorsByWriter := make(chan error, writers)
	var writersDone sync.WaitGroup
	for writer := range writers {
		writersDone.Go(func() {
			<-start
			errorsByWriter <- insertParticipant(t.Context(), pool, fmt.Sprintf("participant_%d", writer), "session_01", "speaker_01", nil)
		})
	}
	close(start)
	writersDone.Wait()
	close(errorsByWriter)

	successes := 0
	conflicts := 0
	for err := range errorsByWriter {
		if err == nil {
			successes++
			continue
		}
		assertPostgresCode(t, err, "23505")
		conflicts++
	}
	if successes != 1 || conflicts != writers-1 {
		t.Fatalf("concurrent participant inserts = %d successes, %d conflicts; want 1 success and %d conflicts", successes, conflicts, writers-1)
	}
}

func testProviderSpeakerConstraint(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	providerSpeakerID := "cluster_01"
	if err := insertParticipant(t.Context(), pool, "participant_provider_01", "session_01", "speaker_02", &providerSpeakerID); err != nil {
		t.Fatalf("insert participant with provider key: %v", err)
	}
	err := insertParticipant(t.Context(), pool, "participant_provider_02", "session_01", "speaker_03", &providerSpeakerID)
	assertPostgresCode(t, err, "23505")
}

func testTurnConstraints(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	createdAt := time.Date(2026, time.July, 27, 10, 0, 0, 0, time.UTC)
	if err := insertTurn(t.Context(), pool, "turn_01", "event_01", "session_01", nil, 1, createdAt); err != nil {
		t.Fatalf("insert voice turn: %v", err)
	}
	assertPostgresCode(t, insertTurnWithPayloadHash(t.Context(), pool, "turn_hash", "event_hash", "session_hash", nil, 1, createdAt, []byte{1}), "23514")
	assertPostgresCode(t, insertTurn(t.Context(), pool, "turn_02", "event_01", "session_02", nil, 1, createdAt), "23505")
	assertPostgresCode(t, insertTurn(t.Context(), pool, "turn_03", "event_03", "session_01", nil, 1, createdAt), "23505")

	missingParticipantID := "participant_missing"
	assertPostgresCode(t, insertTurn(t.Context(), pool, "turn_04", "event_04", "session_01", &missingParticipantID, 2, createdAt), "23503")

	_, err := pool.Exec(t.Context(), "UPDATE voice_turns SET source_text = 'edited' WHERE id = 'turn_01'")
	if err == nil {
		t.Fatal("updating immutable source_text succeeded, want an error")
	}
	if _, err := pool.Exec(t.Context(), "UPDATE voice_turns SET attribution_status = 'confirmed', speaker_confidence = 0.9 WHERE id = 'turn_01'"); err != nil {
		t.Fatalf("updating attribution fields: %v", err)
	}
}

func insertParticipant(ctx context.Context, pool *pgxpool.Pool, id, sessionID, speakerCode string, providerSpeakerID *string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO voice_session_participants (
			id, session_id, speaker_code, provider_speaker_id, created_at, updated_at
		) VALUES ($1, $2, $3, $4, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		id,
		sessionID,
		speakerCode,
		providerSpeakerID,
	)
	return err
}

func insertTurn(ctx context.Context, pool *pgxpool.Pool, id, eventID, sessionID string, participantID *string, sequenceNo int64, createdAt time.Time) error {
	return insertTurnWithPayloadHash(ctx, pool, id, eventID, sessionID, participantID, sequenceNo, createdAt, make([]byte, 32))
}

func insertTurnWithPayloadHash(ctx context.Context, pool *pgxpool.Pool, id, eventID, sessionID string, participantID *string, sequenceNo int64, createdAt time.Time, payloadHash []byte) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO voice_turns (
			id, event_id, event_payload_hash, session_id, participant_id, speaker_code, sequence_no,
			source_language, target_language, language_config_version, source_text,
			translated_text, attribution_status, started_at, ended_at, created_at
		) VALUES (
			$1, $2, $3, $4, $5, 'speaker_01', $6,
			'zh-CN', 'en-US', 1, 'source',
			'translation', 'pending', $7, $7, $7
		)`,
		id,
		eventID,
		payloadHash,
		sessionID,
		participantID,
		sequenceNo,
		createdAt,
	)
	return err
}

func assertPostgresCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("PostgreSQL error = nil, want SQLSTATE %s", want)
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		t.Fatalf("error = %v, want PostgreSQL SQLSTATE %s", err, want)
	}
	if postgresError.Code != want {
		t.Fatalf("PostgreSQL SQLSTATE = %s, want %s", postgresError.Code, want)
	}
}
