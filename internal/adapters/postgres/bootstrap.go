package postgresadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

// BootstrapRequest defines the explicit local-development control plane.
type BootstrapRequest struct {
	TenantID           string
	TenantSlug         string
	TenantName         string
	PrincipalID        string
	PrincipalSubject   string
	PrincipalName      string
	KnowledgeBaseID    string
	KnowledgeBaseSlug  string
	KnowledgeBaseName  string
	DefaultLanguage    string
	Profiles           ports.ProcessingProfileIDs
	EmbeddingDimension int
	CreatedAt          time.Time
}

// BootstrapLocal idempotently creates the single-tenant records used by the verifiable release.
func (s *Store) BootstrapLocal(ctx context.Context, request BootstrapRequest) error {
	if err := validateBootstrapRequest(request); err != nil {
		return err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("begin bootstrap transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	exec := func(query string, arguments ...any) error {
		if _, err := tx.Exec(ctx, query, arguments...); err != nil {
			return err
		}
		return nil
	}
	if err := exec(`
INSERT INTO tenants (id, slug, name, status, created_at, updated_at)
VALUES ($1, $2, $3, 'active', $4, $4)
ON CONFLICT (id) DO UPDATE
SET slug = EXCLUDED.slug, name = EXCLUDED.name, status = 'active', deleted_at = NULL, updated_at = EXCLUDED.updated_at`,
		request.TenantID, request.TenantSlug, request.TenantName, request.CreatedAt); err != nil {
		return fmt.Errorf("bootstrap tenant: %w", err)
	}
	if err := exec(`
INSERT INTO principals (
    id, tenant_id, kind, external_subject, display_name, status, created_at, updated_at
)
VALUES ($1, $2, 'user', $3, $4, 'active', $5, $5)
ON CONFLICT (id) DO UPDATE
SET external_subject = EXCLUDED.external_subject,
    display_name = EXCLUDED.display_name,
    status = 'active',
    deleted_at = NULL,
    updated_at = EXCLUDED.updated_at`,
		request.PrincipalID, request.TenantID, request.PrincipalSubject, request.PrincipalName, request.CreatedAt); err != nil {
		return fmt.Errorf("bootstrap principal: %w", err)
	}
	if err := exec(`
INSERT INTO knowledge_bases (
    id, tenant_id, slug, name, description, status, revision,
    default_language, created_by, created_at, updated_at
)
VALUES ($1, $2, $3, $4, 'Local verifiable knowledge base', 'active', 1, $5, $6, $7, $7)
ON CONFLICT (id) DO UPDATE
SET slug = EXCLUDED.slug,
    name = EXCLUDED.name,
    default_language = EXCLUDED.default_language,
    status = 'active',
    deleted_at = NULL,
    updated_at = EXCLUDED.updated_at`,
		request.KnowledgeBaseID,
		request.TenantID,
		request.KnowledgeBaseSlug,
		request.KnowledgeBaseName,
		request.DefaultLanguage,
		request.PrincipalID,
		request.CreatedAt,
	); err != nil {
		return fmt.Errorf("bootstrap knowledge base: %w", err)
	}
	if err := exec(`
INSERT INTO kb_acl_bindings (
    id, tenant_id, kb_id, principal_id, role, permissions_json, created_by, created_at
)
VALUES ($1, $2, $3, $4, 'owner', '["knowledge.search","knowledge.read","knowledge.write"]'::jsonb, $4, $5)
ON CONFLICT (kb_id, principal_id) WHERE revoked_at IS NULL DO UPDATE
SET role = 'owner',
    permissions_json = EXCLUDED.permissions_json,
    revoked_at = NULL`,
		"acl_"+request.KnowledgeBaseID+"_"+request.PrincipalID,
		request.TenantID,
		request.KnowledgeBaseID,
		request.PrincipalID,
		request.CreatedAt,
	); err != nil {
		return fmt.Errorf("bootstrap ACL: %w", err)
	}

	profiles := []struct {
		id       string
		kind     string
		name     string
		provider string
		config   map[string]any
	}{
		{request.Profiles.Parser, "parser", "native-text-markdown", "native", map[string]any{"max_bytes": 16777216}},
		{request.Profiles.Chunker, "chunker", "structural", "builtin", map[string]any{"target_tokens": 512, "minimum_tokens": 128, "maximum_tokens": 900, "overlap_tokens": 80}},
		{request.Profiles.Embedding, "embedding", "deterministic-hash", "deterministic", map[string]any{"dimension": request.EmbeddingDimension, "warning": "verification-only, not semantic production retrieval"}},
		{request.Profiles.Index, "index", "redis-hnsw", "redis", map[string]any{"dimension": request.EmbeddingDimension, "vector_type": "FLOAT32", "distance_metric": "COSINE"}},
	}
	for _, profile := range profiles {
		configuration, err := json.Marshal(profile.config)
		if err != nil {
			return fmt.Errorf("encode %s profile: %w", profile.kind, err)
		}
		fingerprint := profile.id
		if err := exec(`
INSERT INTO processing_profiles (
    id, tenant_id, kind, name, version, status, provider,
    configuration_json, fingerprint, created_at
)
VALUES ($1, NULL, $2, $3, '1', 'active', $4, $5::jsonb, $6, $7)
ON CONFLICT (id) DO NOTHING`,
			profile.id,
			profile.kind,
			profile.name,
			profile.provider,
			string(configuration),
			fingerprint,
			request.CreatedAt,
		); err != nil {
			return fmt.Errorf("bootstrap %s profile: %w", profile.kind, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit bootstrap transaction: %w", err)
	}
	return nil
}

func validateBootstrapRequest(request BootstrapRequest) error {
	values := []string{
		request.TenantID,
		request.TenantSlug,
		request.TenantName,
		request.PrincipalID,
		request.PrincipalSubject,
		request.PrincipalName,
		request.KnowledgeBaseID,
		request.KnowledgeBaseSlug,
		request.KnowledgeBaseName,
		request.DefaultLanguage,
		request.Profiles.Parser,
		request.Profiles.Chunker,
		request.Profiles.Embedding,
		request.Profiles.Index,
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("bootstrap request contains an empty required field")
		}
	}
	if request.EmbeddingDimension < 16 || request.EmbeddingDimension > 4096 || request.CreatedAt.IsZero() {
		return fmt.Errorf("bootstrap dimension or time is invalid")
	}
	return nil
}
