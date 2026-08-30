package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	postgresadapter "github.com/mss-boot-io/mss-knowledge/internal/adapters/postgres"
	"github.com/mss-boot-io/mss-knowledge/internal/buildinfo"
	"github.com/mss-boot-io/mss-knowledge/internal/config"
	"github.com/mss-boot-io/mss-knowledge/internal/foundation"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "mss-knowledge-ctl: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		printUsage()
		return nil
	}

	switch arguments[0] {
	case "version":
		return writeJSON(buildinfo.Current())
	case "config":
		if len(arguments) != 2 || arguments[1] != "check" {
			return fmt.Errorf("usage: mss-knowledge-ctl config check")
		}
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("configuration is invalid: %w", err)
		}
		return writeJSON(map[string]any{
			"status":       "valid",
			"service":      cfg.ServiceName,
			"environment":  cfg.Environment,
			"http_address": cfg.HTTP.Address,
		})
	case "bootstrap":
		if len(arguments) != 2 || arguments[1] != "local" {
			return fmt.Errorf("usage: mss-knowledge-ctl bootstrap local")
		}
		return bootstrapLocal()
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", arguments[0])
	}
}

func bootstrapLocal() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if cfg.Database.URL == "" {
		return fmt.Errorf("MSS_KNOWLEDGE_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := postgresadapter.Open(ctx, postgresadapter.Config{
		URL:             cfg.Database.URL,
		ApplicationName: "mss-knowledge-ctl",
		MaxConnections:  2,
		MinConnections:  1,
	})
	if err != nil {
		return fmt.Errorf("open PostgreSQL: %w", err)
	}
	defer store.Close()

	tenantID := cfg.Auth.TenantID
	principalID := cfg.Auth.PrincipalID
	if tenantID == "" {
		tenantID = foundation.LocalTenantID
	}
	if principalID == "" {
		principalID = foundation.LocalPrincipalID
	}
	request := postgresadapter.BootstrapRequest{
		TenantID:          tenantID,
		TenantSlug:        "local",
		TenantName:        "Local MSS Knowledge",
		PrincipalID:       principalID,
		PrincipalSubject:  "local-verifier",
		PrincipalName:     "Local Verifier",
		KnowledgeBaseID:   foundation.LocalKnowledgeBaseID,
		KnowledgeBaseSlug: "local",
		KnowledgeBaseName: "Local Verification",
		DefaultLanguage:   "chinese",
		Profiles: ports.ProcessingProfileIDs{
			Parser:    cfg.Processing.ParserProfileID,
			Chunker:   cfg.Processing.ChunkerProfileID,
			Embedding: cfg.Processing.EmbeddingProfileID,
			Index:     cfg.Processing.IndexProfileID,
		},
		EmbeddingDimension: cfg.Embedding.Dimension,
		CreatedAt:          time.Now().UTC(),
	}
	if err := store.BootstrapLocal(ctx, request); err != nil {
		return fmt.Errorf("bootstrap local control plane: %w", err)
	}
	return writeJSON(map[string]any{
		"status":            "ready",
		"tenant_id":         request.TenantID,
		"principal_id":      request.PrincipalID,
		"knowledge_base_id": request.KnowledgeBaseID,
	})
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func printUsage() {
	_, _ = fmt.Fprintln(os.Stdout, `mss-knowledge-ctl

Usage:
  mss-knowledge-ctl version
  mss-knowledge-ctl config check
  mss-knowledge-ctl bootstrap local

Planned commands:
  mss-knowledge-ctl index rebuild --all
  mss-knowledge-ctl index rebuild --knowledge-base <id>
  mss-knowledge-ctl document reindex --document <id>`)
}
