package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mss-boot-io/mss-knowledge/internal/buildinfo"
	"github.com/mss-boot-io/mss-knowledge/internal/config"
	"github.com/mss-boot-io/mss-knowledge/internal/runtimeapp"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "mss-knowledge-worker: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	logger := runtimeapp.NewJSONLogger(cfg.LogLevel).With(
		"service", "mss-knowledge-worker",
		"environment", cfg.Environment,
		"version", buildinfo.Current().Version,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Warn("foundation mode: ingestion repository and stage adapters are not wired; no jobs will be claimed")
	<-ctx.Done()
	logger.Info("worker stopped")
	return nil
}
