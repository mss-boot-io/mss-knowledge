package postgresadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mss-boot-io/mss-knowledge/internal/domain/document"
	"github.com/mss-boot-io/mss-knowledge/internal/domain/ingestion"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

var (
	// ErrWritePermissionDenied is returned when the control plane rejects a write.
	ErrWritePermissionDenied = fmt.Errorf("%w: knowledge-base write permission denied", ports.ErrPermissionDenied)
	// ErrLeaseLost is returned when a stale worker attempts to persist job state.
	ErrLeaseLost = errors.New("ingestion job lease lost")
)

// CanWriteKnowledgeBase checks current principal and ACL state for document ingestion.
func (s *Store) CanWriteKnowledgeBase(
	ctx context.Context,
	tenantID string,
	principalID string,
	knowledgeBaseID string,
) (bool, error) {
	tenantID = strings.TrimSpace(tenantID)
	principalID = strings.TrimSpace(principalID)
	knowledgeBaseID = strings.TrimSpace(knowledgeBaseID)
	if tenantID == "" || principalID == "" || knowledgeBaseID == "" {
		return false, ErrInvalidPrincipal
	}
	var allowed bool
	err := s.pool.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM knowledge_bases AS kb
    JOIN principals AS principal
      ON principal.tenant_id = kb.tenant_id
     AND principal.id = $2
    JOIN kb_acl_bindings AS acl
      ON acl.tenant_id = kb.tenant_id
     AND acl.kb_id = kb.id
     AND acl.principal_id = principal.id
    WHERE kb.tenant_id = $1
      AND kb.id = $3
      AND kb.status = 'active'
      AND kb.deleted_at IS NULL
      AND principal.status = 'active'
      AND principal.deleted_at IS NULL
      AND acl.revoked_at IS NULL
      AND (
          acl.role IN ('owner', 'admin', 'editor')
          OR acl.permissions_json ? 'knowledge.write'
      )
)`, tenantID, principalID, knowledgeBaseID).Scan(&allowed)
	if err != nil {
		return false, fmt.Errorf("check knowledge-base write permission: %w", err)
	}
	return allowed, nil
}

// CreateUpload atomically creates a logical document, processing version, and ingestion job.
// The source bytes must already be stored in S3; callers delete that object if this transaction fails.
func (s *Store) CreateUpload(
	ctx context.Context,
	request ports.CreateUploadRequest,
) (ports.CreateUploadResult, error) {
	if err := validateCreateUpload(request); err != nil {
		return ports.CreateUploadResult{}, err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return ports.CreateUploadResult{}, fmt.Errorf("begin upload transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var allowed bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM knowledge_bases AS kb
    JOIN principals AS principal
      ON principal.tenant_id = kb.tenant_id
     AND principal.id = $2
    JOIN kb_acl_bindings AS acl
      ON acl.tenant_id = kb.tenant_id
     AND acl.kb_id = kb.id
     AND acl.principal_id = principal.id
    WHERE kb.tenant_id = $1
      AND kb.id = $3
      AND kb.status = 'active'
      AND kb.deleted_at IS NULL
      AND principal.status = 'active'
      AND principal.deleted_at IS NULL
      AND acl.revoked_at IS NULL
      AND (
          acl.role IN ('owner', 'admin', 'editor')
          OR acl.permissions_json ? 'knowledge.write'
      )
)`, request.TenantID, request.PrincipalID, request.KnowledgeBaseID).Scan(&allowed); err != nil {
		return ports.CreateUploadResult{}, fmt.Errorf("authorize upload transaction: %w", err)
	}
	if !allowed {
		return ports.CreateUploadResult{}, ErrWritePermissionDenied
	}

	if _, err := tx.Exec(ctx, `
INSERT INTO documents (
    id, tenant_id, kb_id, source_id, external_key, title, status,
    created_by, created_at, updated_at
)
VALUES ($1, $2, $3, NULL, $4, $5, 'active', $6, $7, $7)`,
		request.DocumentID,
		request.TenantID,
		request.KnowledgeBaseID,
		request.ExternalKey,
		request.Title,
		request.PrincipalID,
		request.CreatedAt,
	); err != nil {
		return ports.CreateUploadResult{}, fmt.Errorf("insert document: %w", err)
	}

	const versionNumber int64 = 1
	if _, err := tx.Exec(ctx, `
INSERT INTO document_versions (
    id, tenant_id, kb_id, document_id, version_number, status,
    source_uri, object_bucket, object_key, object_version_id,
    filename, media_type, size_bytes, content_sha256,
    parser_profile_id, chunker_profile_id, embedding_profile_id, index_profile_id,
    pipeline_fingerprint, created_by, created_at
)
VALUES (
    $1, $2, $3, $4, $5, 'PROCESSING',
    $6, $7, $8, $9,
    $10, $11, $12, $13,
    $14, $15, $16, $17,
    $18, $19, $20
)`,
		request.VersionID,
		request.TenantID,
		request.KnowledgeBaseID,
		request.DocumentID,
		versionNumber,
		request.SourceURI,
		request.Original.Bucket,
		request.Original.Key,
		request.Original.VersionID,
		request.Filename,
		request.MediaType,
		request.Original.Size,
		request.Original.SHA256,
		request.Profiles.Parser,
		request.Profiles.Chunker,
		request.Profiles.Embedding,
		request.Profiles.Index,
		request.PipelineFingerprint,
		request.PrincipalID,
		request.CreatedAt,
	); err != nil {
		return ports.CreateUploadResult{}, fmt.Errorf("insert document version: %w", err)
	}

	stageData, err := json.Marshal(map[string]any{
		"source_uri": request.SourceURI,
		"object": map[string]any{
			"bucket":     request.Original.Bucket,
			"key":        request.Original.Key,
			"version_id": request.Original.VersionID,
			"etag":       request.Original.ETag,
			"size":       request.Original.Size,
			"sha256":     request.Original.SHA256,
			"media_type": request.Original.MediaType,
		},
	})
	if err != nil {
		return ports.CreateUploadResult{}, fmt.Errorf("encode upload stage data: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO ingestion_jobs (
    id, tenant_id, kb_id, document_id, version_id, kind,
    state, current_stage, priority, attempt, max_attempts,
    input_fingerprint, stage_data_json, created_at, next_attempt_at, updated_at
)
VALUES (
    $1, $2, $3, $4, $5, 'ingest',
    'PENDING', 'RECEIVED', 0, 0, 5,
    $6, $7::jsonb, $8, $8, $8
)`,
		request.JobID,
		request.TenantID,
		request.KnowledgeBaseID,
		request.DocumentID,
		request.VersionID,
		request.PipelineFingerprint,
		string(stageData),
		request.CreatedAt,
	); err != nil {
		return ports.CreateUploadResult{}, fmt.Errorf("insert ingestion job: %w", err)
	}

	if _, err := tx.Exec(ctx, `
INSERT INTO audit_events (
    tenant_id, principal_id, action, resource_type, resource_id, outcome, metadata_json, created_at
)
VALUES ($1, $2, 'knowledge.document.upload.accepted', 'document_version', $3, 'accepted', $4::jsonb, $5)`,
		request.TenantID,
		request.PrincipalID,
		request.VersionID,
		fmt.Sprintf(`{"job_id":%q,"knowledge_base_id":%q}`, request.JobID, request.KnowledgeBaseID),
		request.CreatedAt,
	); err != nil {
		return ports.CreateUploadResult{}, fmt.Errorf("insert upload audit event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ports.CreateUploadResult{}, fmt.Errorf("commit upload transaction: %w", err)
	}
	return ports.CreateUploadResult{
		DocumentID:    request.DocumentID,
		VersionID:     request.VersionID,
		JobID:         request.JobID,
		VersionNumber: versionNumber,
	}, nil
}

// ClaimNext leases one due job using SKIP LOCKED and attempt fencing.
func (s *Store) ClaimNext(
	ctx context.Context,
	workerID string,
	now time.Time,
	leaseDuration time.Duration,
) (*ingestion.Job, error) {
	workerID = strings.TrimSpace(workerID)
	now = now.UTC()
	if workerID == "" || now.IsZero() || leaseDuration <= 0 {
		return nil, fmt.Errorf("invalid ingestion lease request")
	}
	leaseExpiresAt := now.Add(leaseDuration)

	row := s.pool.QueryRow(ctx, `
WITH candidate AS (
    SELECT id
    FROM ingestion_jobs
    WHERE (
        (state IN ('PENDING', 'RETRY_WAIT') AND next_attempt_at <= $2)
        OR (state = 'RUNNING' AND lease_expires_at <= $2)
    )
      AND attempt < max_attempts
    ORDER BY priority DESC, next_attempt_at, created_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE ingestion_jobs AS job
SET state = 'RUNNING',
    attempt = job.attempt + 1,
    lease_owner = $1,
    lease_expires_at = $3,
    started_at = COALESCE(job.started_at, $2),
    error_code = '',
    error_message = '',
    updated_at = $2
FROM candidate
WHERE job.id = candidate.id
RETURNING
    job.id, job.tenant_id, job.kb_id, job.document_id, job.version_id,
    job.kind, job.state, job.current_stage, job.attempt, job.max_attempts,
    job.lease_owner, job.lease_expires_at, job.next_attempt_at,
    job.error_code, job.error_message, job.created_at, job.started_at,
    job.completed_at, job.updated_at`, workerID, now, leaseExpiresAt)

	job, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim ingestion job: %w", err)
	}
	return &job, nil
}

// Save persists job progress only while the attempt fence still matches.
func (s *Store) Save(ctx context.Context, job ingestion.Job) error {
	if err := job.Validate(); err != nil {
		return err
	}
	command, err := s.pool.Exec(ctx, `
UPDATE ingestion_jobs
SET state = $3,
    current_stage = $4,
    lease_owner = $5,
    lease_expires_at = $6,
    next_attempt_at = $7,
    error_code = $8,
    error_message = $9,
    started_at = $10,
    completed_at = $11,
    updated_at = $12
WHERE id = $1
  AND attempt = $2`,
		string(job.ID),
		job.Attempt,
		string(job.State),
		string(job.Stage),
		job.LeaseOwner,
		job.LeaseExpiresAt,
		job.NextAttemptAt,
		job.ErrorCode,
		job.ErrorMessage,
		job.StartedAt,
		job.CompletedAt,
		job.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("save ingestion job: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

// GetJob returns one tenant-scoped ingestion job.
func (s *Store) GetJob(ctx context.Context, tenantID string, jobID ingestion.JobID) (ingestion.Job, error) {
	row := s.pool.QueryRow(ctx, `
SELECT
    id, tenant_id, kb_id, document_id, version_id,
    kind, state, current_stage, attempt, max_attempts,
    lease_owner, lease_expires_at, next_attempt_at,
    error_code, error_message, created_at, started_at,
    completed_at, updated_at
FROM ingestion_jobs
WHERE tenant_id = $1 AND id = $2`, strings.TrimSpace(tenantID), strings.TrimSpace(string(jobID)))
	job, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ingestion.Job{}, ports.ErrNotFound
	}
	if err != nil {
		return ingestion.Job{}, fmt.Errorf("read ingestion job: %w", err)
	}
	return job, nil
}

// LoadVersionInput returns the immutable source and configured processing profiles for a worker.
func (s *Store) LoadVersionInput(ctx context.Context, tenantID string, versionID string) (ports.VersionInput, error) {
	var input ports.VersionInput
	err := s.pool.QueryRow(ctx, `
SELECT
    version.tenant_id,
    version.kb_id,
    version.document_id,
    version.id,
    document.title,
    version.filename,
    version.media_type,
    kb.default_language,
    version.source_uri,
    version.object_bucket,
    version.object_key,
    version.object_version_id,
    version.size_bytes,
    version.content_sha256,
    version.parser_profile_id,
    version.chunker_profile_id,
    version.embedding_profile_id,
    version.index_profile_id,
    version.pipeline_fingerprint
FROM document_versions AS version
JOIN documents AS document
  ON document.tenant_id = version.tenant_id
 AND document.id = version.document_id
JOIN knowledge_bases AS kb
  ON kb.tenant_id = version.tenant_id
 AND kb.id = version.kb_id
WHERE version.tenant_id = $1
  AND version.id = $2
  AND version.deleted_at IS NULL
  AND document.deleted_at IS NULL
  AND kb.deleted_at IS NULL`, strings.TrimSpace(tenantID), strings.TrimSpace(versionID)).Scan(
		&input.TenantID,
		&input.KnowledgeBaseID,
		&input.DocumentID,
		&input.VersionID,
		&input.Title,
		&input.Filename,
		&input.MediaType,
		&input.DefaultLanguage,
		&input.SourceURI,
		&input.Original.Bucket,
		&input.Original.Key,
		&input.Original.VersionID,
		&input.Original.Size,
		&input.Original.SHA256,
		&input.Profiles.Parser,
		&input.Profiles.Chunker,
		&input.Profiles.Embedding,
		&input.Profiles.Index,
		&input.PipelineFingerprint,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.VersionInput{}, ports.ErrNotFound
	}
	if err != nil {
		return ports.VersionInput{}, fmt.Errorf("load document version input: %w", err)
	}
	input.Original.MediaType = input.MediaType
	return input, nil
}

// ReplaceVersionChunks replaces the rebuildable PostgreSQL chunk catalog for one version.
func (s *Store) ReplaceVersionChunks(
	ctx context.Context,
	tenantID string,
	versionID string,
	chunks []ports.StoredChunk,
) error {
	tenantID = strings.TrimSpace(tenantID)
	versionID = strings.TrimSpace(versionID)
	if tenantID == "" || versionID == "" {
		return fmt.Errorf("tenant and version IDs are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin chunk-catalog transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `DELETE FROM chunks WHERE tenant_id = $1 AND version_id = $2`, tenantID, versionID); err != nil {
		return fmt.Errorf("clear version chunk catalog: %w", err)
	}
	for index, stored := range chunks {
		headingPath, err := json.Marshal(stored.Chunk.HeadingPath)
		if err != nil {
			return fmt.Errorf("encode chunk %d heading path: %w", index, err)
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO chunks (
    id, tenant_id, kb_id, document_id, version_id, parent_chunk_id,
    ordinal, content_type, heading_path_json, page_start, page_end,
    content_sha256, text_object_key, token_count
)
VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8, $9::jsonb, $10, $11, $12, $13, $14)`,
			stored.Chunk.ID,
			tenantID,
			stored.Chunk.KnowledgeBaseID,
			stored.Chunk.DocumentID,
			stored.Chunk.VersionID,
			stored.Chunk.ParentChunkID,
			stored.Chunk.Ordinal,
			string(stored.Chunk.ContentType),
			string(headingPath),
			stored.Chunk.PageStart,
			stored.Chunk.PageEnd,
			stored.Chunk.ContentSHA256,
			stored.TextObjectKey,
			stored.Chunk.TokenCount,
		); err != nil {
			return fmt.Errorf("insert chunk %d: %w", index, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit chunk-catalog transaction: %w", err)
	}
	return nil
}

// SaveArtifacts records immutable S3 outputs before publication.
func (s *Store) SaveArtifacts(
	ctx context.Context,
	versionID document.VersionID,
	artifacts ports.VersionArtifacts,
) error {
	command, err := s.pool.Exec(ctx, `
UPDATE document_versions
SET normalized_object_key = $2,
    normalized_object_version_id = $3,
    normalized_sha256 = $4,
    manifest_object_key = $5,
    manifest_object_version_id = $6,
    manifest_sha256 = $7,
    chunk_count = $8,
    token_count = $9
WHERE id = $1
  AND status = 'PROCESSING'
  AND deleted_at IS NULL`,
		string(versionID),
		artifacts.Normalized.Key,
		artifacts.Normalized.VersionID,
		artifacts.Normalized.SHA256,
		artifacts.Manifest.Key,
		artifacts.Manifest.VersionID,
		artifacts.Manifest.SHA256,
		artifacts.ChunkCount,
		artifacts.TokenCount,
	)
	if err != nil {
		return fmt.Errorf("save version artifacts: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ports.ErrNotFound
	}
	return nil
}

// PublishVersion atomically switches the active version and emits durable events.
func (s *Store) PublishVersion(ctx context.Context, request ports.PublishVersionRequest) error {
	publishedAt := request.PublishedAt.UTC()
	if request.TenantID == "" || request.KnowledgeBaseID == "" || request.DocumentID == "" || request.VersionID == "" || publishedAt.IsZero() {
		return fmt.Errorf("invalid publication request")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("begin publication transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var currentVersionID *string
	if err := tx.QueryRow(ctx, `
SELECT active_version_id
FROM documents
WHERE tenant_id = $1 AND kb_id = $2 AND id = $3 AND status = 'active' AND deleted_at IS NULL
FOR UPDATE`, request.TenantID, request.KnowledgeBaseID, string(request.DocumentID)).Scan(&currentVersionID); errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrNotFound
	} else if err != nil {
		return fmt.Errorf("lock document for publication: %w", err)
	}

	var status string
	if err := tx.QueryRow(ctx, `
SELECT status
FROM document_versions
WHERE tenant_id = $1 AND kb_id = $2 AND document_id = $3 AND id = $4 AND deleted_at IS NULL
FOR UPDATE`, request.TenantID, request.KnowledgeBaseID, string(request.DocumentID), string(request.VersionID)).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrNotFound
	} else if err != nil {
		return fmt.Errorf("lock version for publication: %w", err)
	}
	if status == string(document.VersionStatusReady) && currentVersionID != nil && *currentVersionID == string(request.VersionID) {
		return tx.Commit(ctx)
	}
	if status != string(document.VersionStatusProcessing) {
		return fmt.Errorf("document version %s is %s, want PROCESSING", request.VersionID, status)
	}

	if currentVersionID != nil && *currentVersionID != "" && *currentVersionID != string(request.VersionID) {
		if _, err := tx.Exec(ctx, `
UPDATE document_versions
SET status = 'SUPERSEDED', superseded_at = $2
WHERE id = $1 AND status = 'READY'`, *currentVersionID, publishedAt); err != nil {
			return fmt.Errorf("supersede previous document version: %w", err)
		}
	}
	command, err := tx.Exec(ctx, `
UPDATE document_versions
SET status = 'READY', published_at = $2, error_code = '', error_detail_json = '{}'::jsonb
WHERE id = $1
  AND status = 'PROCESSING'
  AND normalized_object_key <> ''
  AND normalized_object_version_id <> ''
  AND manifest_object_key <> ''
  AND manifest_object_version_id <> ''
  AND chunk_count > 0`, string(request.VersionID), publishedAt)
	if err != nil {
		return fmt.Errorf("publish document version: %w", err)
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("document version artifacts are incomplete")
	}
	if _, err := tx.Exec(ctx, `
UPDATE documents
SET active_version_id = $2, updated_at = $3
WHERE id = $1`, string(request.DocumentID), string(request.VersionID), publishedAt); err != nil {
		return fmt.Errorf("switch active document version: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE knowledge_bases
SET revision = revision + 1, updated_at = $2
WHERE tenant_id = $1 AND id = $3`, request.TenantID, publishedAt, request.KnowledgeBaseID); err != nil {
		return fmt.Errorf("increment knowledge-base revision: %w", err)
	}
	payload, _ := json.Marshal(map[string]any{
		"tenant_id":         request.TenantID,
		"knowledge_base_id": request.KnowledgeBaseID,
		"document_id":       request.DocumentID,
		"version_id":        request.VersionID,
	})
	if _, err := tx.Exec(ctx, `
INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, payload_json, occurred_at)
VALUES ('document', $1, 'knowledge.document.version.published', $2::jsonb, $3)`,
		string(request.DocumentID), string(payload), publishedAt); err != nil {
		return fmt.Errorf("insert publication outbox event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit publication transaction: %w", err)
	}
	return nil
}

// MarkVersionFailed records a terminal processing failure.
func (s *Store) MarkVersionFailed(
	ctx context.Context,
	versionID document.VersionID,
	code string,
	detail map[string]any,
) error {
	encoded, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode version failure detail: %w", err)
	}
	command, err := s.pool.Exec(ctx, `
UPDATE document_versions
SET status = 'FAILED', error_code = $2, error_detail_json = $3::jsonb
WHERE id = $1 AND status = 'PROCESSING'`, string(versionID), strings.TrimSpace(code), string(encoded))
	if err != nil {
		return fmt.Errorf("mark document version failed: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ports.ErrNotFound
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(row rowScanner) (ingestion.Job, error) {
	var job ingestion.Job
	var id string
	var state string
	var stage string
	err := row.Scan(
		&id,
		&job.TenantID,
		&job.KnowledgeBaseID,
		&job.DocumentID,
		&job.VersionID,
		&job.Kind,
		&state,
		&stage,
		&job.Attempt,
		&job.MaxAttempts,
		&job.LeaseOwner,
		&job.LeaseExpiresAt,
		&job.NextAttemptAt,
		&job.ErrorCode,
		&job.ErrorMessage,
		&job.CreatedAt,
		&job.StartedAt,
		&job.CompletedAt,
		&job.UpdatedAt,
	)
	if err != nil {
		return ingestion.Job{}, err
	}
	job.ID = ingestion.JobID(id)
	job.State = ingestion.State(state)
	job.Stage = ingestion.Stage(stage)
	return job, nil
}

func validateCreateUpload(request ports.CreateUploadRequest) error {
	values := []string{
		request.TenantID,
		request.PrincipalID,
		request.KnowledgeBaseID,
		request.DocumentID,
		request.VersionID,
		request.JobID,
		request.ExternalKey,
		request.Title,
		request.Filename,
		request.MediaType,
		request.SourceURI,
		request.Original.Bucket,
		request.Original.Key,
		request.Original.VersionID,
		request.Original.SHA256,
		request.Profiles.Parser,
		request.Profiles.Chunker,
		request.Profiles.Embedding,
		request.Profiles.Index,
		request.PipelineFingerprint,
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("create upload request contains an empty required field")
		}
	}
	if request.Original.Size <= 0 || request.CreatedAt.IsZero() {
		return fmt.Errorf("create upload request size or time is invalid")
	}
	return nil
}

var _ ports.UploadRepository = (*Store)(nil)
var _ ports.IngestionReader = (*Store)(nil)
var _ ports.JobRepository = (*Store)(nil)
var _ ports.ChunkRepository = (*Store)(nil)
var _ ports.KnowledgeBaseWriteAuthorizer = (*Store)(nil)
