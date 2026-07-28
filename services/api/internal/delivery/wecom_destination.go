package delivery

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
	"github.com/oklog/ulid/v2"
)

// WeComBotDestinationConfigurer owns the authenticated account-level setup
// workflow. The public API only accepts a reference and webhook URL; it never
// exposes the stored provider target after this one-time request.
type WeComBotDestinationConfigurer interface {
	ConfigureWeComBot(context.Context, string, string, string) error
}

type WeComBotDestinationVerifier interface {
	VerifyWeComBotDestination(context.Context, string) error
}

type WeComBotDestinationService struct {
	destinations *PostgresDestinationReader
	verifier     WeComBotDestinationVerifier
}

func NewWeComBotDestinationService(destinations *PostgresDestinationReader, verifier WeComBotDestinationVerifier) *WeComBotDestinationService {
	return &WeComBotDestinationService{destinations: destinations, verifier: verifier}
}

func (s *WeComBotDestinationService) ConfigureWeComBot(ctx context.Context, accountID, reference, webhookURL string) error {
	if s == nil || s.destinations == nil || s.verifier == nil {
		return domain.ErrNotImplemented
	}
	if accountID == "" || !validDestinationReference(reference) {
		return domain.ErrInvalidArgument
	}
	if err := validateWeComBotWebhook(webhookURL); err != nil {
		return domain.ErrInvalidArgument
	}
	if err := s.verifier.VerifyWeComBotDestination(ctx, webhookURL); err != nil {
		return err
	}
	return s.destinations.putVerifiedDestination(ctx, accountID, ChannelWeComBot, reference, webhookURL)
}

func validDestinationReference(reference string) bool {
	return len(reference) > 0 && len(reference) <= 100 && strings.TrimSpace(reference) == reference
}

func (r *PostgresDestinationReader) putVerifiedDestination(ctx context.Context, accountID string, channel Channel, reference, target string) error {
	if r == nil || r.pool == nil || accountID == "" || !IsSupportedChannel(channel) || !validDestinationReference(reference) || target == "" {
		return domain.ErrInvalidArgument
	}
	ciphertext, err := EncryptProviderTarget(r.key, target)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = r.pool.Exec(ctx, `
		INSERT INTO account_destinations (id, account_id, channel, destination_ref, provider_target_ciphertext, key_version, verified_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$7,$7)
		ON CONFLICT (account_id, channel, destination_ref)
		DO UPDATE SET provider_target_ciphertext=EXCLUDED.provider_target_ciphertext,key_version=EXCLUDED.key_version,verified_at=EXCLUDED.verified_at,revoked_at=NULL,updated_at=EXCLUDED.updated_at`,
		"dest_"+ulid.Make().String(), accountID, channel, reference, ciphertext, "v1", now,
	)
	if err != nil {
		return fmt.Errorf("store verified enterprise WeChat destination: %w", err)
	}
	return nil
}

var _ WeComBotDestinationConfigurer = (*WeComBotDestinationService)(nil)
