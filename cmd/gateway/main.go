package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	staticauth "github.com/mss-boot-io/mss-knowledge/internal/adapters/auth/static"
	"github.com/mss-boot-io/mss-knowledge/internal/adapters/embedding/deterministic"
	"github.com/mss-boot-io/mss-knowledge/internal/adapters/idgen"
	postgresadapter "github.com/mss-boot-io/mss-knowledge/internal/adapters/postgres"
	"github.com/mss-boot-io/mss-knowledge/internal/adapters/redissearch"
	"github.com/mss-boot-io/mss-knowledge/internal/adapters/s3store"
	catalogapp "github.com/mss-boot-io/mss-knowledge/internal/app/catalog"
	fetchapp "github.com/mss-boot-io/mss-knowledge/internal/app/fetch"
	ingestionapp "github.com/mss-boot-io/mss-knowledge/internal/app/ingestion"
	searchapp "github.com/mss-boot-io/mss-knowledge/internal/app/search"
	"github.com/mss-boot-io/mss-knowledge/internal/buildinfo"
	"github.com/mss-boot-io/mss-knowledge/internal/config"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
	"github.com/mss-boot-io/mss-knowledge/internal/runtimeapp"
	"github.com/mss-boot-io/mss-knowledge/internal/transport/httpapi"
	"github.com/mss-boot-io/mss-knowledge/internal/transport/mcpserver"
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
	if err := cfg.ValidateGateway(); err != nil {
		return fmt.Errorf("validate gateway configuration: %w", err)
	}
	logger := runtimeapp.NewJSONLogger(cfg.LogLevel).With(
		"service", "mss-knowledge-gateway",
		"environment", cfg.Environment,
	)

	rootContext, cancel := context.WithCancel(context.Background())
	defer cancel()

	postgres, err := postgresadapter.Open(rootContext, postgresadapter.Config{
		URL:             cfg.Database.URL,
		ApplicationName: "mss-knowledge-gateway",
		MaxConnections:  cfg.Database.MaxConnections,
		MinConnections:  cfg.Database.MinConnections,
	})
	if err != nil {
		return fmt.Errorf("open PostgreSQL: %w", err)
	}
	defer postgres.Close()

	embeddingProfile := ports.EmbeddingProfile{
		Provider:      cfg.Embedding.Provider,
		ModelID:       cfg.Embedding.Model,
		ModelRevision: "1",
		Dimension:     cfg.Embedding.Dimension,
		VectorType:    "FLOAT32",
		Normalize:     true,
		BatchSize:     64,
		Fingerprint:   fmt.Sprintf("%s:%s:%d:float32:l2", cfg.Embedding.Provider, cfg.Embedding.Model, cfg.Embedding.Dimension),
	}
	embedder := deterministic.Provider{}
	if err := embedder.Check(rootContext); err != nil {
		return fmt.Errorf("check embedding provider: %w", err)
	}

	redisStore, err := redissearch.Open(rootContext, redissearch.Config{
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
	defer redisStore.Close()

	objectStore, err := s3store.Open(rootContext, s3store.Config{
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

	ids := idgen.Random{}
	searchService, err := searchapp.New(redisStore, postgres, postgres, ids, searchapp.Config{
		MaxTopK:             cfg.Search.MaxTopK,
		CandidateMultiplier: cfg.Search.CandidateMultiplier,
		MaxHitsPerDocument:  cfg.Search.MaxHitsPerDocument,
	})
	if err != nil {
		return fmt.Errorf("create search service: %w", err)
	}
	fetchService, err := fetchapp.New(redisStore, postgres, postgres)
	if err != nil {
		return fmt.Errorf("create fetch service: %w", err)
	}
	catalogService, err := catalogapp.New(postgres)
	if err != nil {
		return fmt.Errorf("create catalog service: %w", err)
	}
	ingestionService, err := ingestionapp.New(
		postgres,
		postgres,
		objectStore,
		postgres,
		postgres,
		ids,
		ingestionapp.Config{
			Bucket:   cfg.S3.Bucket,
			MaxBytes: cfg.HTTP.MaxRequestBytes,
			Profiles: ports.ProcessingProfileIDs{
				Parser:    cfg.Processing.ParserProfileID,
				Chunker:   cfg.Processing.ChunkerProfileID,
				Embedding: cfg.Processing.EmbeddingProfileID,
				Index:     cfg.Processing.IndexProfileID,
			},
			EmbeddingModel:     cfg.Embedding.Model,
			EmbeddingDimension: cfg.Embedding.Dimension,
		},
	)
	if err != nil {
		return fmt.Errorf("create ingestion service: %w", err)
	}

	principalResolver, err := staticauth.New(staticauth.Config{
		Token:       cfg.Auth.StaticToken,
		TenantID:    cfg.Auth.TenantID,
		PrincipalID: cfg.Auth.PrincipalID,
		Scopes:      cfg.Auth.Scopes,
	})
	if err != nil {
		return fmt.Errorf("create principal resolver: %w", err)
	}

	mcpHandler, err := mcpserver.NewHandler(mcpserver.Options{
		Name:    "mss-knowledge",
		Version: buildinfo.Current().Version,
		Search:  searchService,
		Fetch:   fetchService,
		Catalog: catalogService,
	})
	if err != nil {
		return fmt.Errorf("create MCP server: %w", err)
	}
	mcpHandler = principalResolver.Middleware(mcpHandler)

	transport, err := httpapi.New(httpapi.Options{
		Logger:          logger,
		Build:           buildinfo.Current(),
		Search:          searchService,
		Fetch:           fetchService,
		Catalog:         catalogService,
		Ingestion:       ingestionService,
		Principals:      principalResolver,
		MCP:             mcpHandler,
		Readiness:       []httpapi.ReadinessProbe{postgres, redisStore, objectStore},
		IDs:             ids,
		MaxRequestBytes: cfg.HTTP.MaxRequestBytes,
	})
	if err != nil {
		return fmt.Errorf("create HTTP transport: %w", err)
	}

	server := &http.Server{
		Addr:              cfg.HTTP.Address,
		Handler:           transport.Handler(),
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
	}

	ctx, stop := signal.NotifyContext(rootContext, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("gateway dependencies initialized",
		"auth_mode", cfg.Auth.Mode,
		"embedding_provider", cfg.Embedding.Provider,
		"embedding_model", cfg.Embedding.Model,
		"embedding_dimension", cfg.Embedding.Dimension,
		"redis_index", cfg.Redis.IndexName,
	)
	if err := runtimeapp.ServeHTTP(ctx, logger, server, cfg.ShutdownTimeout); err != nil {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	logger.Info("gateway stopped")
	return nil
}
