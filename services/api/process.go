package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/1024XEngineer/xe6-tsy/services/api/languages"
	"github.com/1024XEngineer/xe6-tsy/services/api/recordstore"
)

// runWorkerProcess keeps the delivery dispatcher and consumer out of the API
// process when Compose runs the two roles as separate services. The context is
// signal-owned so both goroutines stop before the database and Valkey clients
// are closed by the deferred cleanup.
func runWorkerProcess() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runtime, err := newPersistentRuntime(ctx)
	if err != nil {
		return err
	}
	defer runtime.close()
	select {
	case err := <-runtime.run(ctx):
		return err
	case <-ctx.Done():
		return nil
	}
}

// runMigrationProcess applies both API schema histories as a one-shot job.
// Keeping this separate from the long-running services makes deployment order
// explicit while retaining idempotence through each module's migration table.
func runMigrationProcess() error {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required for migration process")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := recordstore.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := recordstore.Migrate(ctx, pool); err != nil {
		return err
	}
	if err := languages.ApplyMigrations(ctx, pool); err != nil {
		return err
	}
	slog.Info("Lingow database migrations applied")
	return nil
}
