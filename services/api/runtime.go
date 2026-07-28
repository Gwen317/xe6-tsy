package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/accounts"
	"github.com/1024XEngineer/xe6-tsy/services/api/internal/delivery"
	"github.com/1024XEngineer/xe6-tsy/services/api/internal/usage"
	"github.com/1024XEngineer/xe6-tsy/services/api/recordstore"
)

type persistentRuntime struct {
	accounts   accounts.Service
	verifier   accounts.AccessTokenVerifier
	usage      usage.Service
	delivery   delivery.Service
	wecom      delivery.WeComBotDestinationConfigurer
	worker     *delivery.Worker
	dispatcher *delivery.OutboxDispatcher
	pool       *pgxpool.Pool
	redis      *redis.Client
}

func newPersistentRuntime(ctx context.Context) (*persistentRuntime, error) {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	valkeyURL := strings.TrimSpace(os.Getenv("VALKEY_URL"))
	if databaseURL == "" || valkeyURL == "" {
		return nil, fmt.Errorf("DATABASE_URL and VALKEY_URL are required for persistent runtime")
	}
	pool, err := recordstore.Open(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	cleanupPool := true
	defer func() {
		if cleanupPool == false {
			return
		}
		pool.Close()
	}()
	if err := recordstore.Migrate(ctx, pool); err != nil {
		return nil, err
	}

	redisOptions, err := redis.ParseURL(valkeyURL)
	if err != nil {
		return nil, fmt.Errorf("parse VALKEY_URL: %w", err)
	}
	redisClient := redis.NewClient(redisOptions)
	if err := redisClient.Ping(ctx).Err(); err != nil {
		redisClient.Close()
		return nil, fmt.Errorf("ping Valkey: %w", err)
	}

	accountRepository := accounts.NewPostgresRepository(pool)
	secret := os.Getenv("LINGOW_TOKEN_SECRET")
	issuer, err := accounts.NewHMACIssuerWithAccount(secret, envOr("LINGOW_TOKEN_ISSUER", "lingow-api"), envOr("LINGOW_TOKEN_AUDIENCE", "lingow-client"), accountRepository.SessionActiveForAccount)
	if err != nil {
		redisClient.Close()
		return nil, err
	}
	verificationSender, err := newVerificationSenderFromEnv()
	if err != nil {
		redisClient.Close()
		return nil, err
	}
	digester, err := accounts.NewCredentialDigester(os.Getenv("LINGOW_AUTH_PEPPER"))
	if err != nil {
		redisClient.Close()
		return nil, fmt.Errorf("configure LINGOW_AUTH_PEPPER: %w", err)
	}
	accountService := accounts.NewPersistentUseCases(accountRepository, issuer, issuer, verificationSender, digester)

	destinationKey, err := delivery.DecodeDestinationKey(os.Getenv("LINGOW_DESTINATION_KEY"))
	if err != nil {
		redisClient.Close()
		return nil, fmt.Errorf("decode LINGOW_DESTINATION_KEY: %w", err)
	}
	destinationReader, err := delivery.NewPostgresDestinationReader(pool, destinationKey)
	if err != nil {
		redisClient.Close()
		return nil, err
	}
	queue, err := delivery.NewValkeyQueue(ctx, redisClient, envOr("LINGOW_DELIVERY_STREAM", "lingow:delivery"), envOr("LINGOW_DELIVERY_GROUP", "api"), envOr("LINGOW_DELIVERY_CONSUMER", hostname()))
	if err != nil {
		redisClient.Close()
		return nil, fmt.Errorf("initialize Valkey queue: %w", err)
	}
	deliveryRepository := delivery.NewPostgresRepository(pool)
	turnReader := delivery.NewPostgresTurnReader(pool)
	provider, providerName, err := newDeliveryProviderFromEnv()
	if err != nil {
		redisClient.Close()
		return nil, err
	}
	var wecomConfigurer delivery.WeComBotDestinationConfigurer
	if verifier, ok := provider.(delivery.WeComBotDestinationVerifier); ok {
		wecomConfigurer = delivery.NewWeComBotDestinationService(destinationReader, verifier)
	}
	deliveryService := delivery.NewPersistentUseCases(deliveryRepository, turnReader, destinationReader, queue)
	usageService := usage.NewPersistentUseCases(usage.NewPostgresRepository(pool), accountRepository)
	worker := delivery.NewWorker(queue, delivery.WorkerDependencies{Repository: deliveryRepository, Destinations: destinationReader, Provider: provider})
	dispatcher := delivery.NewOutboxDispatcher(deliveryRepository, queue, time.Second)
	cleanupPool = false
	slog.Info("persistent member5 runtime enabled", "provider", providerName)
	return &persistentRuntime{accounts: accountService, verifier: issuer, usage: usageService, delivery: deliveryService, wecom: wecomConfigurer, worker: worker, dispatcher: dispatcher, pool: pool, redis: redisClient}, nil
}

// run starts background delivery loops and reports their first unexpected
// failure. A process supervisor must treat that failure as fatal; silently
// keeping a healthy-looking worker container after consumption stops would lose
// the recovery guarantee of the durable outbox.
func (r *persistentRuntime) run(ctx context.Context) <-chan error {
	errors := make(chan error, 2)
	go func() {
		if err := r.dispatcher.Run(ctx); err != nil {
			select {
			case errors <- fmt.Errorf("delivery outbox dispatcher: %w", err):
			case <-ctx.Done():
			}
		}
	}()
	go func() {
		if err := r.worker.Run(ctx); err != nil {
			select {
			case errors <- fmt.Errorf("delivery worker: %w", err):
			case <-ctx.Done():
			}
		}
	}()
	return errors
}

func (r *persistentRuntime) close() {
	if r == nil {
		return
	}
	if r.redis != nil {
		_ = r.redis.Close()
	}
	if r.pool != nil {
		r.pool.Close()
	}
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func newVerificationSenderFromEnv() (accounts.VerificationSender, error) {
	switch strings.ToLower(envOr("LINGOW_SMS_PROVIDER", "disabled")) {
	case "", "disabled":
		return nil, nil
	case "mock-webhook":
		if strings.ToLower(envOr("LINGOW_APP_ENV", "production")) != "development" {
			return nil, fmt.Errorf("LINGOW_SMS_PROVIDER=mock-webhook requires LINGOW_APP_ENV=development")
		}
		return accounts.NewWebhookVerificationSender(os.Getenv("LINGOW_SMS_WEBHOOK_URL"))
	default:
		return nil, fmt.Errorf("unsupported LINGOW_SMS_PROVIDER %q", os.Getenv("LINGOW_SMS_PROVIDER"))
	}
}

func newDeliveryProviderFromEnv() (delivery.Provider, string, error) {
	switch strings.ToLower(envOr("LINGOW_DELIVERY_PROVIDER", "unconfigured")) {
	case "", "unconfigured", "disabled":
		return delivery.UnconfiguredProvider{}, "unconfigured", nil
	case "wecom-bot":
		return delivery.NewWeComBotProvider(), "wecom-bot", nil
	default:
		return nil, "", fmt.Errorf("unsupported LINGOW_DELIVERY_PROVIDER %q", os.Getenv("LINGOW_DELIVERY_PROVIDER"))
	}
}

func hostname() string {
	value, err := os.Hostname()
	if err != nil || value == "" {
		return "worker-1"
	}
	return value
}
