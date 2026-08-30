package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/mss-boot-io/mss-knowledge/internal/adapters/idgen"
	"github.com/mss-boot-io/mss-knowledge/internal/buildinfo"
	"github.com/mss-boot-io/mss-knowledge/internal/config"
	"github.com/mss-boot-io/mss-knowledge/internal/runtimeapp"
	"github.com/mss-boot-io/mss-knowledge/internal/transport/httpapi"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "mss-knowledge-gateway: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	logger := runtimeapp.NewJSONLogger(cfg.LogLevel).With(
		"service", "mss-knowledge-gateway",
		"environment", cfg.Environment,
	)

	transport, err := httpapi.New(httpapi.Options{
		Logger:          logger,
		Build:           buildinfo.Current(),
		IDs:             idgen.Random{},
		MaxRequestBytes: cfg.HTTP.MaxRequestBytes,
	})
	if err != nil {
		return fmt.Errorf("create HTTP transport: %w", err)
	}

	logger.Warn("foundation mode: authentication and search adapters are not wired; /v1/search returns unavailable")

	server := &http.Server{
		Addr:              cfg.HTTP.Address,
		Handler:           transport.Handler(),
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runtimeapp.ServeHTTP(ctx, logger, server, cfg.ShutdownTimeout); err != nil {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	logger.Info("gateway stopped")
	return nil
}
