package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mss-boot-io/mss-knowledge/internal/adapters/embedding/deterministic"
	nativeparser "github.com/mss-boot-io/mss-knowledge/internal/adapters/parser/native"
	postgresadapter "github.com/mss-boot-io/mss-knowledge/internal/adapters/postgres"
	"github.com/mss-boot-io/mss-knowledge/internal/adapters/redissearch"
	"github.com/mss-boot-io/mss-knowledge/internal/adapters/s3store"
	heuristictokenizer "github.com/mss-boot-io/mss-knowledge/internal/adapters/tokenizer/heuristic"
	"github.com/mss-boot-io/mss-knowledge/internal/app/chunking"
	"github.com/mss-boot-io/mss-knowledge/internal/app/processing"
	workerapp "github.com/mss-boot-io/mss-knowledge/internal/app/worker"
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
	if err := cfg.ValidateWorker(); err != nil {
		return fmt.Errorf("validate worker configuration: %w", err)
	}
	logger := runtimeapp.NewJSONLogger(cfg.LogLevel).With(
		"service", "mss-knowledge-worker",
		"environment", cfg.Environment,
		"version", buildinfo.Current().Version,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	postgres, err := postgresadapter.Open(ctx, postgresadapter.Config{
		URL:             cfg.Database.URL,
		ApplicationName: "mss-knowledge-worker",
		MaxConnections:  cfg.Database.MaxConnections,
		MinConnections:  cfg.Database.MinConnections,
	})
	if err != nil {
		return fmt.Errorf("open PostgreSQL: %w", err)
	}
	defer postgres.Close()

	objects, err := s3store.Open(ctx, s3store.Config{
		Endpoint:          cfg.S3.Endpoint,
		Region:            cfg.S3.Region,
		Bucket:            cfg.S3.Bucket,
		AccessKeyID:       cfg.S3.AccessKeyID,
		SecretAccessKey:   cfg.S3.SecretAccessKey,
		SessionToken:      cfg.S3.SessionToken,
		PathStyle:         cfg.S3.PathStyle,
		RequireVersioning: cfg.S3.RequireVersioning,
	})
	if err != nil {
		return fmt.Errorf("open S3 object store: %w", err)
	}

	profileIDs, chunkProfile, embeddingProfile := runtimeapp.ProcessingProfiles(cfg)
	embedder := deterministic.Provider{}
	if err := embedder.Check(ctx); err != nil {
		return fmt.Errorf("check embedding provider: %w", err)
	}
	index, err := redissearch.Open(ctx, redissearch.Config{
		Address:          cfg.Redis.Address,
		Username:         cfg.Redis.Username,
		Password:         cfg.Redis.Password,
		Database:         cfg.Redis.Database,
		TLS:              cfg.Redis.TLS,
		IndexName:        cfg.Redis.IndexName,
		KeyPrefix:        cfg.Redis.KeyPrefix,
		EmbeddingProfile: embeddingProfile,
	}, embedder)
	if err != nil {
		return fmt.Errorf("open Redis search store: %w", err)
	}
	defer index.Close()

	parser, err := nativeparser.New(nativeparser.Config{MaxBytes: cfg.HTTP.MaxRequestBytes})
	if err != nil {
		return fmt.Errorf("create native parser: %w", err)
	}
	chunker, err := chunking.NewStructural(heuristictokenizer.Counter{})
	if err != nil {
		return fmt.Errorf("create structural chunker: %w", err)
	}
	pipeline, err := processing.New(objects, parser, chunker, embedder, processing.Config{
		MaxSourceBytes:   cfg.HTTP.MaxRequestBytes,
		Profiles:         profileIDs,
		ChunkProfile:     chunkProfile,
		EmbeddingProfile: embeddingProfile,
	})
	if err != nil {
		return fmt.Errorf("create processing pipeline: %w", err)
	}

	workerID := strings.TrimSpace(cfg.Worker.ID)
	if workerID == "" {
		workerID = defaultWorkerID()
	}
	service, err := workerapp.New(
		postgres,
		postgres,
		objects,
		postgres,
		postgres,
		index,
		pipeline,
		logger,
		workerapp.Config{
			WorkerID:       workerID,
			PollInterval:   cfg.Worker.PollInterval,
			LeaseDuration:  cfg.Worker.LeaseDuration,
			RetryBase:      cfg.Worker.RetryBase,
			RetryMaximum:   cfg.Worker.RetryMaximum,
			ArtifactPrefix: cfg.S3.Prefix,
			Bucket:         cfg.S3.Bucket,
			IndexVersion:   cfg.Processing.IndexProfileID,
		},
	)
	if err != nil {
		return fmt.Errorf("create ingestion worker: %w", err)
	}

	logger.InfoContext(ctx, "worker dependencies initialized",
		"worker_id", workerID,
		"poll_interval", cfg.Worker.PollInterval,
		"lease_duration", cfg.Worker.LeaseDuration,
		"redis_index", cfg.Redis.IndexName,
		"s3_bucket", cfg.S3.Bucket,
	)
	if err := service.Run(ctx); err != nil {
		return fmt.Errorf("run ingestion worker: %w", err)
	}
	logger.Info("worker stopped")
	return nil
}

func defaultWorkerID() string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "unknown-host"
	}
	return fmt.Sprintf("%s-%d", hostname, os.Getpid())
}
