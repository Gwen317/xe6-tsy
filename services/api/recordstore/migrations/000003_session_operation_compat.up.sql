-- Forward-only compatibility fixes for the session contract introduced after
-- 000002 had already been deployed. Historical start-request rows represent
-- successful activations, so they are backfilled as completed operations before
-- the new invariants become mandatory.

ALTER TABLE voice_sessions
    DROP CONSTRAINT voice_sessions_timestamps_valid;

ALTER TABLE voice_sessions
    ADD CONSTRAINT voice_sessions_timestamps_valid CHECK (
        (status = 'created' AND started_at IS NULL AND ended_at IS NULL AND failure_error_code IS NULL)
        OR (status = 'active' AND started_at IS NOT NULL AND ended_at IS NULL AND failure_error_code IS NULL)
        OR (
            status = 'ended'
            AND ended_at IS NOT NULL
            AND failure_error_code IS NULL
            AND (
                (started_at IS NULL AND ended_at >= created_at)
                OR (started_at IS NOT NULL AND ended_at >= started_at)
            )
        )
        OR (status = 'failed' AND started_at IS NOT NULL AND ended_at IS NULL AND failure_error_code IS NOT NULL)
    );

ALTER TABLE voice_session_start_requests
    ADD COLUMN operation_id TEXT,
    ADD COLUMN status TEXT,
    ADD COLUMN compensation_claim_id TEXT,
    ADD COLUMN updated_at TIMESTAMPTZ;

UPDATE voice_session_start_requests
SET operation_id = 'legacy:' || idempotency_key,
    status = 'completed',
    updated_at = GREATEST(created_at, started_at);

ALTER TABLE voice_session_start_requests
    ALTER COLUMN operation_id SET NOT NULL,
    ALTER COLUMN status SET NOT NULL,
    ALTER COLUMN started_at DROP NOT NULL,
    ALTER COLUMN updated_at SET DEFAULT CURRENT_TIMESTAMP,
    ALTER COLUMN updated_at SET NOT NULL,
    ADD CONSTRAINT voice_session_start_requests_operation_id_not_empty CHECK (operation_id <> ''),
    ADD CONSTRAINT voice_session_start_requests_status_valid CHECK (
        status IN ('pending', 'compensating', 'completed', 'compensated', 'compensation_failed')
    ),
    ADD CONSTRAINT voice_session_start_requests_claim_id_not_empty CHECK (
        compensation_claim_id IS NULL OR compensation_claim_id <> ''
    ),
    ADD CONSTRAINT voice_session_start_requests_updated_at_valid CHECK (updated_at >= created_at),
    ADD CONSTRAINT voice_session_start_requests_state_valid CHECK (
        (status = 'pending' AND started_at IS NULL AND compensation_claim_id IS NULL)
        OR (status = 'compensating' AND started_at IS NULL AND compensation_claim_id IS NOT NULL)
        OR (status = 'completed' AND started_at IS NOT NULL AND compensation_claim_id IS NULL)
        OR (
            status IN ('compensated', 'compensation_failed')
            AND started_at IS NULL
            AND compensation_claim_id IS NOT NULL
        )
    ),
    ADD CONSTRAINT voice_session_start_requests_account_operation_key
        UNIQUE (account_id, operation_id);
