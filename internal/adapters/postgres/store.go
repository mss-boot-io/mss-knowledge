package postgresadapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mss-boot-io/mss-knowledge/internal/domain/catalog"
	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
)

const (
	defaultApplicationName    = "mss-knowledge"
	defaultMaxConnections     = int32(10)
	defaultMinConnections     = int32(1)
	defaultConnectTimeout     = 5 * time.Second
	defaultMaxConnLifetime    = 30 * time.Minute
	defaultMaxConnIdleTime    = 5 * time.Minute
	defaultHealthCheckPeriod  = time.Minute
	maxRequestedKnowledgeBase = 1000
	maxActiveVersionBatch     = 2000
)

var (
	// ErrInvalidConfig is returned before opening an invalid PostgreSQL pool.
	ErrInvalidConfig = errors.New("invalid PostgreSQL configuration")
	// ErrInvalidPrincipal is returned when authoritative queries lack an internal identity.
	ErrInvalidPrincipal = errors.New("invalid search principal")
	// ErrBatchTooLarge is returned when an unbounded caller would create an unsafe SQL array.
	ErrBatchTooLarge = errors.New("PostgreSQL query batch is too large")
)

// Config controls the PostgreSQL connection pool.
type Config struct {
	URL                   string
	ApplicationName       string
	MaxConnections        int32
	MinConnections        int32
	ConnectTimeout        time.Duration
	MaxConnectionLifetime time.Duration
	MaxConnectionIdleTime time.Duration
	HealthCheckPeriod     time.Duration
}

// Store is the PostgreSQL control-plane adapter.
type Store struct {
	pool *pgxpool.Pool
}

// Open creates and verifies a PostgreSQL connection pool.
func Open(ctx context.Context, config Config) (*Store, error) {
	config = config.withDefaults()
	if err := config.validate(); err != nil {
		return nil, err
	}

	poolConfig, err := pgxpool.ParseConfig(config.URL)
	if err != nil {
		return nil, fmt.Errorf("%w: parse connection URL: %v", ErrInvalidConfig, err)
	}
	poolConfig.MaxConns = config.MaxConnections
	poolConfig.MinConns = config.MinConnections
	poolConfig.MaxConnLifetime = config.MaxConnectionLifetime
	poolConfig.MaxConnIdleTime = config.MaxConnectionIdleTime
	poolConfig.HealthCheckPeriod = config.HealthCheckPeriod
	poolConfig.ConnConfig.ConnectTimeout = config.ConnectTimeout
	poolConfig.ConnConfig.RuntimeParams["application_name"] = config.ApplicationName

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL pool: %w", err)
	}

	pingContext, cancel := context.WithTimeout(ctx, config.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingContext); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases every PostgreSQL connection.
func (s *Store) Close() {
	if s == nil || s.pool == nil {
		return
	}
	s.pool.Close()
}

// Name implements the HTTP readiness-probe contract.
func (s *Store) Name() string {
	return "postgres"
}

// Check verifies that PostgreSQL can serve a control-plane query.
func (s *Store) Check(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("PostgreSQL store is not initialized")
	}
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return nil
}

// AllowedKnowledgeBases returns the authoritative intersection of requested and readable knowledge bases.
func (s *Store) AllowedKnowledgeBases(
	ctx context.Context,
	principal searchdomain.Principal,
	requested []string,
) ([]string, error) {
	if err := validatePrincipal(principal); err != nil {
		return nil, err
	}
	requested = compactUnique(requested)
	if len(requested) > maxRequestedKnowledgeBase {
		return nil, fmt.Errorf("%w: requested knowledge bases exceed %d", ErrBatchTooLarge, maxRequestedKnowledgeBase)
	}

	rows, err := s.pool.Query(ctx, `
SELECT kb.id
FROM knowledge_bases AS kb
JOIN kb_acl_bindings AS acl
  ON acl.tenant_id = kb.tenant_id
 AND acl.kb_id = kb.id
JOIN principals AS principal
  ON principal.tenant_id = kb.tenant_id
 AND principal.id = acl.principal_id
WHERE kb.tenant_id = $1
  AND principal.id = $2
  AND principal.status = 'active'
  AND principal.deleted_at IS NULL
  AND kb.status = 'active'
  AND kb.deleted_at IS NULL
  AND acl.revoked_at IS NULL
  AND ($3::boolean OR kb.id = ANY($4::text[]))
  AND (
      acl.role IN ('owner', 'admin', 'editor', 'reader', 'agent')
      OR acl.permissions_json ? 'knowledge.search'
  )
ORDER BY kb.id`,
		principal.TenantID,
		principal.PrincipalID,
		len(requested) == 0,
		requested,
	)
	if err != nil {
		return nil, fmt.Errorf("query allowed knowledge bases: %w", err)
	}
	defer rows.Close()

	knowledgeBaseIDs := make([]string, 0, len(requested))
	for rows.Next() {
		var knowledgeBaseID string
		if err := rows.Scan(&knowledgeBaseID); err != nil {
			return nil, fmt.Errorf("scan allowed knowledge base: %w", err)
		}
		knowledgeBaseIDs = append(knowledgeBaseIDs, knowledgeBaseID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate allowed knowledge bases: %w", err)
	}
	return knowledgeBaseIDs, nil
}

// ListKnowledgeBases returns the authorization-filtered catalog visible to a principal.
func (s *Store) ListKnowledgeBases(
	ctx context.Context,
	principal searchdomain.Principal,
) ([]catalog.KnowledgeBase, error) {
	if err := validatePrincipal(principal); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
SELECT DISTINCT kb.id, kb.slug, kb.name, kb.description, kb.revision, kb.default_language, kb.updated_at
FROM knowledge_bases AS kb
JOIN kb_acl_bindings AS acl
  ON acl.tenant_id = kb.tenant_id
 AND acl.kb_id = kb.id
JOIN principals AS principal
  ON principal.tenant_id = kb.tenant_id
 AND principal.id = acl.principal_id
WHERE kb.tenant_id = $1
  AND principal.id = $2
  AND principal.status = 'active'
  AND principal.deleted_at IS NULL
  AND kb.status = 'active'
  AND kb.deleted_at IS NULL
  AND acl.revoked_at IS NULL
  AND (
      acl.role IN ('owner', 'admin', 'editor', 'reader', 'agent')
      OR acl.permissions_json ? 'knowledge.search'
  )
ORDER BY kb.name, kb.id`, principal.TenantID, principal.PrincipalID)
	if err != nil {
		return nil, fmt.Errorf("query knowledge-base catalog: %w", err)
	}
	defer rows.Close()

	result := make([]catalog.KnowledgeBase, 0)
	for rows.Next() {
		var item catalog.KnowledgeBase
		if err := rows.Scan(
			&item.ID,
			&item.Slug,
			&item.Name,
			&item.Description,
			&item.Revision,
			&item.DefaultLanguage,
			&item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan knowledge-base catalog: %w", err)
		}
		item.UpdatedAt = item.UpdatedAt.UTC()
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge-base catalog: %w", err)
	}
	return result, nil
}

// CanReadHit revalidates the final hit against current PostgreSQL ACL state.
func (s *Store) CanReadHit(
	ctx context.Context,
	principal searchdomain.Principal,
	hit searchdomain.Hit,
) (bool, error) {
	if err := validatePrincipal(principal); err != nil {
		return false, err
	}
	if strings.TrimSpace(hit.KnowledgeBaseID) == "" || hit.TenantID != principal.TenantID {
		return false, nil
	}

	var allowed bool
	err := s.pool.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM knowledge_bases AS kb
    JOIN kb_acl_bindings AS acl
      ON acl.tenant_id = kb.tenant_id
     AND acl.kb_id = kb.id
    JOIN principals AS principal
      ON principal.tenant_id = kb.tenant_id
     AND principal.id = acl.principal_id
    WHERE kb.tenant_id = $1
      AND kb.id = $2
      AND principal.id = $3
      AND principal.status = 'active'
      AND principal.deleted_at IS NULL
      AND kb.status = 'active'
      AND kb.deleted_at IS NULL
      AND acl.revoked_at IS NULL
      AND (
          acl.role IN ('owner', 'admin', 'editor', 'reader', 'agent')
          OR acl.permissions_json ? 'knowledge.search'
      )
)`,
		principal.TenantID,
		hit.KnowledgeBaseID,
		principal.PrincipalID,
	).Scan(&allowed)
	if err != nil {
		return false, fmt.Errorf("revalidate knowledge-base access: %w", err)
	}
	return allowed, nil
}

// ActiveVersionIDs returns only active, READY versions belonging to active documents and knowledge bases.
func (s *Store) ActiveVersionIDs(
	ctx context.Context,
	tenantID string,
	documentIDs []string,
) (map[string]string, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID must not be empty")
	}
	documentIDs = compactUnique(documentIDs)
	if len(documentIDs) == 0 {
		return map[string]string{}, nil
	}
	if len(documentIDs) > maxActiveVersionBatch {
		return nil, fmt.Errorf("%w: document IDs exceed %d", ErrBatchTooLarge, maxActiveVersionBatch)
	}

	rows, err := s.pool.Query(ctx, `
SELECT document.id, version.id
FROM documents AS document
JOIN document_versions AS version
  ON version.tenant_id = document.tenant_id
 AND version.document_id = document.id
 AND version.id = document.active_version_id
JOIN knowledge_bases AS kb
  ON kb.tenant_id = document.tenant_id
 AND kb.id = document.kb_id
WHERE document.tenant_id = $1
  AND document.id = ANY($2::text[])
  AND document.status = 'active'
  AND document.deleted_at IS NULL
  AND version.status = 'READY'
  AND version.deleted_at IS NULL
  AND kb.status = 'active'
  AND kb.deleted_at IS NULL
ORDER BY document.id`, tenantID, documentIDs)
	if err != nil {
		return nil, fmt.Errorf("query active document versions: %w", err)
	}
	defer rows.Close()

	activeVersions := make(map[string]string, len(documentIDs))
	for rows.Next() {
		var documentID string
		var versionID string
		if err := rows.Scan(&documentID, &versionID); err != nil {
			return nil, fmt.Errorf("scan active document version: %w", err)
		}
		activeVersions[documentID] = versionID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active document versions: %w", err)
	}
	return activeVersions, nil
}

func (c Config) withDefaults() Config {
	c.URL = strings.TrimSpace(c.URL)
	c.ApplicationName = strings.TrimSpace(c.ApplicationName)
	if c.ApplicationName == "" {
		c.ApplicationName = defaultApplicationName
	}
	if c.MaxConnections == 0 {
		c.MaxConnections = defaultMaxConnections
	}
	if c.MinConnections == 0 {
		c.MinConnections = defaultMinConnections
	}
	if c.ConnectTimeout == 0 {
		c.ConnectTimeout = defaultConnectTimeout
	}
	if c.MaxConnectionLifetime == 0 {
		c.MaxConnectionLifetime = defaultMaxConnLifetime
	}
	if c.MaxConnectionIdleTime == 0 {
		c.MaxConnectionIdleTime = defaultMaxConnIdleTime
	}
	if c.HealthCheckPeriod == 0 {
		c.HealthCheckPeriod = defaultHealthCheckPeriod
	}
	return c
}

func (c Config) validate() error {
	if c.URL == "" {
		return fmt.Errorf("%w: URL must not be empty", ErrInvalidConfig)
	}
	if c.MaxConnections <= 0 || c.MinConnections < 0 || c.MinConnections > c.MaxConnections {
		return fmt.Errorf("%w: connection bounds are invalid", ErrInvalidConfig)
	}
	if c.ConnectTimeout <= 0 || c.MaxConnectionLifetime <= 0 ||
		c.MaxConnectionIdleTime <= 0 || c.HealthCheckPeriod <= 0 {
		return fmt.Errorf("%w: durations must be positive", ErrInvalidConfig)
	}
	return nil
}

func validatePrincipal(principal searchdomain.Principal) error {
	if strings.TrimSpace(principal.TenantID) == "" || strings.TrimSpace(principal.PrincipalID) == "" {
		return ErrInvalidPrincipal
	}
	return nil
}

func compactUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
