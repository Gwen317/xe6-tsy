package recordstore

import (
	"strings"
	"testing"
)

func TestEmbeddedMigrations(t *testing.T) {
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatalf("embeddedMigrations() error = %v", err)
	}
	if len(migrations) != 8 {
		t.Fatalf("len(embeddedMigrations()) = %d, want 8", len(migrations))
	}
	voiceRecords := migrations[0]
	if voiceRecords.Version != 1 || voiceRecords.Name != "voice_records" {
		t.Fatalf("migration = %#v, want version 1 named voice_records", voiceRecords)
	}
	for _, table := range []string{"voice_session_participants", "voice_turns"} {
		if !strings.Contains(voiceRecords.SQL, "CREATE TABLE "+table) {
			t.Fatalf("migration SQL does not create %s", table)
		}
	}
	for _, constraint := range []string{"event_payload_hash BYTEA NOT NULL", "octet_length(event_payload_hash) = 32"} {
		if !strings.Contains(voiceRecords.SQL, constraint) {
			t.Fatalf("migration SQL does not contain %q", constraint)
		}
	}

	controlPlane := migrations[1]
	if controlPlane.Version != 2 || controlPlane.Name != "member5_control_plane" {
		t.Fatalf("migration = %#v, want version 2 named member5_control_plane", controlPlane)
	}
	for _, table := range []string{
		"lingow_accounts", "lingow_phone_challenges", "lingow_auth_sessions", "voice_sessions",
		"lingow_usage_records", "outbound_messages", "delivery_attempts", "delivery_outbox",
		"message_preferences", "account_destinations",
	} {
		if !strings.Contains(controlPlane.SQL, "CREATE TABLE "+table) {
			t.Fatalf("migration SQL does not create %s", table)
		}
	}
	for _, constraint := range []string{
		"CREATE UNIQUE INDEX lingow_accounts_phone_hash_key",
		"ON lingow_accounts (phone_hash)",
		"WHERE phone_hash IS NOT NULL",
		"cost_amount NUMERIC(20, 8)",
		"currency TEXT",
		"cost_amount IS NULL OR cost_amount >= 0",
		"currency IS NULL OR currency ~ '^[A-Z]{3}$'",
	} {
		if !strings.Contains(controlPlane.SQL, constraint) {
			t.Fatalf("control-plane migration SQL does not contain %q", constraint)
		}
	}
	if strings.Contains(controlPlane.SQL, "delivery_outbox_idempotency_key UNIQUE") {
		t.Fatal("delivery outbox must not make account-scoped idempotency keys globally unique")
	}
	if !strings.Contains(controlPlane.SQL, "CONSTRAINT delivery_outbox_attempt_key UNIQUE (attempt_id)") {
		t.Fatal("delivery outbox must keep attempt_id as the durable unique identity")
	}

	sessionOperationCompat := migrations[2]
	if sessionOperationCompat.Version != 3 || sessionOperationCompat.Name != "session_operation_compat" {
		t.Fatalf("migration = %#v, want version 3 named session_operation_compat", sessionOperationCompat)
	}
	for _, change := range []string{
		"DROP CONSTRAINT voice_sessions_timestamps_valid",
		"started_at IS NULL AND ended_at >= created_at",
		"ADD COLUMN operation_id TEXT",
		"ADD COLUMN status TEXT",
		"ADD COLUMN compensation_claim_id TEXT",
		"ADD COLUMN updated_at TIMESTAMPTZ",
		"status = 'completed'",
		"ALTER COLUMN started_at DROP NOT NULL",
		"status = 'pending' AND started_at IS NULL",
		"status IN ('pending', 'compensating', 'completed', 'compensated', 'compensation_failed')",
		"UNIQUE (account_id, operation_id)",
	} {
		if !strings.Contains(sessionOperationCompat.SQL, change) {
			t.Fatalf("session-operation compatibility migration SQL does not contain %q", change)
		}
	}

	phoneChallengeHardening := migrations[3]
	if phoneChallengeHardening.Version != 4 || phoneChallengeHardening.Name != "phone_challenge_hardening" {
		t.Fatalf("migration = %#v, want version 4 named phone_challenge_hardening", phoneChallengeHardening)
	}
	for _, change := range []string{
		"ADD COLUMN attempts SMALLINT NOT NULL DEFAULT 0",
		"ADD COLUMN max_attempts SMALLINT NOT NULL DEFAULT 5",
		"ADD COLUMN last_attempt_at TIMESTAMPTZ",
		"CHECK (attempts >= 0 AND attempts <= max_attempts)",
		"CHECK (max_attempts BETWEEN 1 AND 10)",
		"CREATE INDEX lingow_phone_challenges_phone_created_idx",
		"ON lingow_phone_challenges (phone_hash, created_at DESC)",
	} {
		if !strings.Contains(phoneChallengeHardening.SQL, change) {
			t.Fatalf("phone-challenge hardening migration SQL does not contain %q", change)
		}
	}

	accountLineage := migrations[4]
	if accountLineage.Version != 5 || accountLineage.Name != "account_lineage" {
		t.Fatalf("migration = %#v, want version 5 named account_lineage", accountLineage)
	}
	for _, change := range []string{
		"CREATE OR REPLACE FUNCTION lingow_account_lineage",
		"JOIN lineage AS parent ON child.merged_into = parent.id",
		"NOT child.id = ANY(parent.visited)",
	} {
		if !strings.Contains(accountLineage.SQL, change) {
			t.Fatalf("account-lineage migration SQL does not contain %q", change)
		}
	}

	phoneDigestV2 := migrations[5]
	if phoneDigestV2.Version != 6 || phoneDigestV2.Name != "phone_digest_v2" {
		t.Fatalf("migration = %#v, want version 6 named phone_digest_v2", phoneDigestV2)
	}
	for _, change := range []string{
		"ADD COLUMN phone_hash_v2 TEXT",
		"CREATE UNIQUE INDEX lingow_accounts_phone_hash_v2_key",
		"ADD COLUMN legacy_phone_hash TEXT",
		"ADD COLUMN digest_version SMALLINT NOT NULL DEFAULT 1",
		"CHECK (digest_version IN (1, 2))",
	} {
		if !strings.Contains(phoneDigestV2.SQL, change) {
			t.Fatalf("phone-digest v2 migration SQL does not contain %q", change)
		}
	}

	deliveryWeComBot := migrations[6]
	if deliveryWeComBot.Version != 7 || deliveryWeComBot.Name != "delivery_wecom_bot" {
		t.Fatalf("migration = %#v, want version 7 named delivery_wecom_bot", deliveryWeComBot)
	}
	for _, change := range []string{
		"account_destinations_channel_valid", "message_preferences_channel_valid", "outbound_messages_channel_valid", "'wecom_bot'",
	} {
		if !strings.Contains(deliveryWeComBot.SQL, change) {
			t.Fatalf("delivery WeCom migration SQL does not contain %q", change)
		}
	}

	phoneDigestCleanup := migrations[7]
	if phoneDigestCleanup.Version != 8 || phoneDigestCleanup.Name != "phone_digest_cleanup" {
		t.Fatalf("migration = %#v, want version 8 named phone_digest_cleanup", phoneDigestCleanup)
	}
	for _, change := range []string{"UPDATE lingow_accounts", "SET phone_hash = NULL", "phone_hash_v2 IS NOT NULL"} {
		if !strings.Contains(phoneDigestCleanup.SQL, change) {
			t.Fatalf("phone-digest cleanup migration SQL does not contain %q", change)
		}
	}
}
