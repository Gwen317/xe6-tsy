package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/accounts"
	"github.com/1024XEngineer/xe6-tsy/services/api/internal/delivery"
	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
	"github.com/1024XEngineer/xe6-tsy/services/api/internal/usage"
	internalwebapi "github.com/1024XEngineer/xe6-tsy/services/api/internal/webapi"
	"github.com/1024XEngineer/xe6-tsy/services/api/languages"
	recordswebapi "github.com/1024XEngineer/xe6-tsy/services/api/webapi"
)

// main wires foundation use cases into the HTTP server and owns graceful shutdown.
func main() {
	if err := run(); err != nil {
		slog.Error("api exited", "error", err)
		os.Exit(1)
	}
}

func run() error {
	process := strings.TrimSpace(os.Getenv("LINGOW_PROCESS"))
	if len(os.Args) > 1 && !strings.HasPrefix(strings.TrimSpace(os.Args[1]), "-") {
		process = strings.TrimSpace(os.Args[1])
	}
	switch process = strings.ToLower(process); process {
	case "":
		// Keep the historical single-process behavior for `go run .` and local
		// deployments that do not opt into separate API/worker containers.
		return runAPI(true)
	case "combined":
		return runAPI(true)
	case "api":
		return runAPI(false)
	case "worker":
		return runWorkerProcess()
	case "migrate":
		return runMigrationProcess()
	default:
		return fmt.Errorf("unsupported LINGOW_PROCESS %q", process)
	}
}

func runAPI(startBackgroundWorkers bool) error {
	address := os.Getenv("API_ADDR")
	if address == "" {
		address = ":8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hasDatabase := strings.TrimSpace(os.Getenv("DATABASE_URL")) != ""
	hasValkey := strings.TrimSpace(os.Getenv("VALKEY_URL")) != ""
	var runtime *persistentRuntime
	var runtimeErrors <-chan error
	var err error
	if hasDatabase != hasValkey {
		return errors.New("DATABASE_URL and VALKEY_URL must be configured together")
	}
	if hasDatabase {
		runtime, err = newPersistentRuntime(ctx)
		if err != nil {
			return err
		}
		defer runtime.close()
		if startBackgroundWorkers {
			runtimeErrors = runtime.run(ctx)
		}
	}

	langHandler, cleanup, err := newLanguageHandler(ctx)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	mux := buildMux(langHandler, runtime)

	server := &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		slog.Info("Lingow API listening", "address", address)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-runtimeErrors:
		return err
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func newLanguageHandler(ctx context.Context) (*languages.Handler, func(), error) {
	accountID := func(r *http.Request) (string, bool) {
		return internalwebapi.AccountIDFromContext(r.Context())
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Warn("DATABASE_URL unset; language HTTP routes return not_implemented until wired")
		return languages.NewHandler(nil, accountID), nil, nil
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, nil, err
	}
	if err := languages.ApplyMigrations(ctx, pool); err != nil {
		pool.Close()
		return nil, nil, err
	}

	// The HTTP authentication context identifies the caller, but it must not
	// stand in for session ownership. Use the durable session table whenever a
	// database-backed language service is enabled; the trust-auth adapter is
	// retained only for the no-database development path below.
	sessions := postgresLanguageSessionOwner{repository: accounts.NewPostgresRepository(pool)}
	svc := languages.NewService(languages.NewPostgresStore(pool, nil), sessions)
	slog.Info("language configuration service enabled")
	return languages.NewHandler(svc, accountID), pool.Close, nil
}

func sessionOwnerFromEnv() languages.SessionOwnerReader {
	switch os.Getenv("LANGUAGE_SESSION_OWNER") {
	case "trust-auth":
		slog.Warn("LANGUAGE_SESSION_OWNER=trust-auth enabled; sessions are not ownership-checked")
		return languages.TrustAuthSessionOwner{
			AccountIDFromCtx: internalwebapi.AccountIDFromContext,
		}
	default:
		return languages.NotImplementedSessionOwner{}
	}
}

func buildMux(lang *languages.Handler, runtimes ...*persistentRuntime) *http.ServeMux {
	accountUseCases := accounts.Service(accounts.NewUseCases())
	usageUseCases := usage.Service(usage.NewUseCases())
	deliveryUseCases := delivery.Service(delivery.NewUseCases())
	var verifier accounts.AccessTokenVerifier = accountUseCases.(accounts.AccessTokenVerifier)
	if len(runtimes) > 0 && runtimes[0] != nil {
		accountUseCases = runtimes[0].accounts
		usageUseCases = runtimes[0].usage
		deliveryUseCases = runtimes[0].delivery
		verifier = runtimes[0].verifier
	}
	var mux *http.ServeMux
	if len(runtimes) > 0 && runtimes[0] != nil && runtimes[0].wecom != nil {
		mux = internalwebapi.New(accountUseCases, usageUseCases, deliveryUseCases, verifier, runtimes[0].wecom)
	} else {
		mux = internalwebapi.New(accountUseCases, usageUseCases, deliveryUseCases, verifier)
	}
	if len(runtimes) > 0 && runtimes[0] != nil {
		// Module handlers read account identity from context. Mount them behind
		// the same Bearer verifier as member-5 routes so a real HTTP request can
		// reach that context; the more-specific member-5 patterns continue to
		// win over this subtree fallback in net/http ServeMux.
		protected := http.NewServeMux()
		if lang != nil {
			lang.Register(protected)
		}
		recordswebapi.NewNotImplementedHandler(slog.Default()).Register(protected)
		mux.Handle("/api/v1/", internalwebapi.Authenticate(verifier, protected))
	} else {
		// Keep the dependency-free composition used by unit tests and local
		// stubs, where callers deliberately inject a trusted context directly.
		if lang != nil {
			lang.Register(mux)
		}
		recordswebapi.NewNotImplementedHandler(slog.Default()).Register(mux)
	}
	mux.HandleFunc("GET /healthz", healthz)
	return mux
}

// postgresLanguageSessionOwner adapts the account repository's shared session
// ownership query to the language module's error vocabulary.
type postgresLanguageSessionOwner struct {
	repository *accounts.PostgresRepository
}

func (o postgresLanguageSessionOwner) GetOwnerAccountID(ctx context.Context, sessionID string) (string, error) {
	if o.repository == nil {
		return "", languages.ErrNotImplemented
	}
	accountID, err := o.repository.AccountIDForSession(ctx, sessionID)
	if errors.Is(err, domain.ErrNotFound) {
		return "", languages.ErrSessionNotFound
	}
	if err != nil {
		return "", err
	}
	return o.repository.CanonicalAccountID(ctx, accountID)
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
}
