package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/accounts"
	"github.com/1024XEngineer/xe6-tsy/services/api/internal/delivery"
	"github.com/1024XEngineer/xe6-tsy/services/api/internal/usage"
	"github.com/1024XEngineer/xe6-tsy/services/api/internal/webapi"
)

func main() {
	address := os.Getenv("API_ADDR")
	if address == "" {
		address = ":8080"
	}

	webOptions := make([]webapi.Option, 0, 1)
	if secret := os.Getenv("JWT_SECRET"); secret != "" {
		_, verifier, err := accounts.NewHMACTokenManager(secret, "lingow-api", 15*time.Minute)
		if err != nil {
			slog.Error("invalid JWT configuration", "error", err)
			os.Exit(1)
		}
		webOptions = append(webOptions, webapi.WithAccessTokenVerifier(verifier))
	} else {
		slog.Warn("JWT_SECRET is unset; authenticated endpoints cannot derive account context from bearer tokens")
	}

	server := &http.Server{
		Addr:              address,
		Handler:           webapi.New(accounts.NewUseCases(), usage.NewUseCases(), delivery.NewUseCases(), webOptions...),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErrors := make(chan error, 1)
	go func() {
		slog.Info("Lingow API listening", "address", address)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			slog.Error("API server stopped", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("API shutdown failed", "error", err)
			os.Exit(1)
		}
	}
}
