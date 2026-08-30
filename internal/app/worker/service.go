package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/mss-boot-io/mss-knowledge/internal/app/processing"
	"github.com/mss-boot-io/mss-knowledge/internal/domain/document"
	ingestiondomain "github.com/mss-boot-io/mss-knowledge/internal/domain/ingestion"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

var (
	// ErrInvalidDependencies is returned when the worker cannot execute every durable stage.
	ErrInvalidDependencies = errors.New("invalid worker dependencies")
	// ErrInvalidConfig is returned when worker scheduling or object boundaries are unsafe.
	ErrInvalidConfig = errors.New("invalid worker configuration")
	// ErrProjectionCount is returned when Redis does not contain the complete version projection.
	ErrProjectionCount = errors.New("Redis projection count mismatch")
)

// Config controls one durable, single-job-at-a-time worker instance.
type Config struct {
	WorkerID       string
	PollInterval   time.Duration
	LeaseDuration  time.Duration
	RetryBase      time.Duration
	RetryMaximum   time.Duration
	ArtifactPrefix string
	Bucket         string
	IndexVersion   string
}

// Service leases ingestion jobs and publishes complete document versions.
type Service struct {
	jobs      ports.JobRepository
	reader    ports.IngestionReader
	objects   ports.ObjectStore
	chunks    ports.ChunkRepository
	documents ports.DocumentRepository
	index     ports.ChunkIndex
	pipeline  *processing.Pipeline
	logger    *slog.Logger
	config    Config
	now       func() time.Time
}

// New creates a worker service whose durable side effects are all idempotent by version.
func New(
	jobs ports.JobRepository,
	reader ports.IngestionReader,
	objects ports.ObjectStore,
	chunks ports.ChunkRepository,
	documents ports.DocumentRepository,
	index ports.ChunkIndex,
	pipeline *processing.Pipeline,
	logger *slog.Logger,
	config Config,
) (*Service, error) {
	if jobs == nil || reader == nil || objects == nil || chunks == nil || documents == nil || index == nil || pipeline == nil {
		return nil, ErrInvalidDependencies
	}
	config.WorkerID = strings.TrimSpace(config.WorkerID)
	config.ArtifactPrefix = strings.Trim(strings.TrimSpace(config.ArtifactPrefix), "/")
	config.Bucket = strings.TrimSpace(config.Bucket)
	config.IndexVersion = strings.TrimSpace(config.IndexVersion)
	if config.WorkerID == "" || config.Bucket == "" || config.IndexVersion == "" ||
		config.PollInterval <= 0 || config.LeaseDuration <= 0 || config.RetryBase <= 0 || config.RetryMaximum < config.RetryBase {
		return nil, ErrInvalidConfig
	}
	if config.ArtifactPrefix == "" {
		config.ArtifactPrefix = "tenants"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		jobs:      jobs,
		reader:    reader,
		objects:   objects,
		chunks:    chunks,
		documents: documents,
		index:     index,
		pipeline:  pipeline,
		logger:    logger,
		config:    config,
		now:       func() time.Time { return time.Now().UTC() },
	}, nil
}

// Run polls until the context is cancelled. Processing failures are persisted and do not stop the worker.
func (s *Service) Run(ctx context.Context) error {
	for {
		processed, err := s.RunOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			s.logger.ErrorContext(ctx, "ingestion job processing failed", "error", err)
		}
		if err := ctx.Err(); err != nil {
			return nil
		}
		if processed {
			continue
		}
		timer := time.NewTimer(s.config.PollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

// RunOnce claims and processes at most one job.
func (s *Service) RunOnce(ctx context.Context) (bool, error) {
	now := s.now()
	job, err := s.jobs.ClaimNext(ctx, s.config.WorkerID, now, s.config.LeaseDuration)
	if err != nil {
		return false, fmt.Errorf("claim ingestion job: %w", err)
	}
	if job == nil {
		return false, nil
	}

	s.logger.InfoContext(ctx, "ingestion job claimed",
		"job_id", job.ID,
		"tenant_id", job.TenantID,
		"document_id", job.DocumentID,
		"version_id", job.VersionID,
		"attempt", job.Attempt,
		"stage", job.Stage,
	)
	if err := s.process(ctx, job); err != nil {
		if persistErr := s.persistFailure(ctx, job, err); persistErr != nil {
			return true, errors.Join(err, persistErr)
		}
		return true, err
	}
	return true, nil
}

func (s *Service) process(ctx context.Context, job *ingestiondomain.Job) error {
	input, err := s.reader.LoadVersionInput(ctx, job.TenantID, job.VersionID)
	if err != nil {
		return fmt.Errorf("load version input: %w", err)
	}
	if input.DocumentID != job.DocumentID || input.KnowledgeBaseID != job.KnowledgeBaseID {
		return &processing.PermanentError{Code: "VERSION_JOB_MISMATCH", Err: fmt.Errorf("job and document version identities differ")}
	}

	result, err := s.pipeline.Build(ctx, input, func(checkpointContext context.Context, stage ingestiondomain.Stage) error {
		return s.advanceTo(checkpointContext, job, stage)
	})
	if err != nil {
		return err
	}

	artifacts, err := s.persistArtifacts(ctx, input, result)
	if err != nil {
		return err
	}
	storedChunks := make([]ports.StoredChunk, len(result.Chunks))
	for index, chunk := range result.Chunks {
		storedChunks[index] = ports.StoredChunk{Chunk: chunk, TextObjectKey: artifacts.Manifest.Key}
	}
	if err := s.chunks.ReplaceVersionChunks(ctx, input.TenantID, input.VersionID, storedChunks); err != nil {
		return fmt.Errorf("persist chunk catalog: %w", err)
	}
	if err := s.documents.SaveArtifacts(ctx, document.VersionID(input.VersionID), artifacts); err != nil {
		return fmt.Errorf("persist version artifacts: %w", err)
	}

	projected := make([]ports.IndexedChunk, len(result.Chunks))
	indexedAt := s.now()
	for index, chunk := range result.Chunks {
		projected[index] = ports.IndexedChunk{
			TenantID:        input.TenantID,
			KnowledgeBaseID: input.KnowledgeBaseID,
			DocumentID:      input.DocumentID,
			VersionID:       input.VersionID,
			IndexVersion:    s.config.IndexVersion,
			Chunk:           chunk,
			Language:        result.Document.Language,
			Title:           result.Document.Title,
			SourceURI:       input.SourceURI,
			UpdatedAt:       indexedAt,
			Embedding:       result.Embeddings[index],
		}
	}
	if err := s.index.DeleteVersion(ctx, input.TenantID, input.VersionID, s.config.IndexVersion); err != nil {
		return fmt.Errorf("clear partial Redis projection: %w", err)
	}
	if err := s.index.IndexBatch(ctx, projected); err != nil {
		return fmt.Errorf("index Redis projection: %w", err)
	}
	if err := s.advanceTo(ctx, job, ingestiondomain.StageIndexing); err != nil {
		return err
	}
	count, err := s.index.CountVersion(ctx, input.TenantID, input.VersionID, s.config.IndexVersion)
	if err != nil {
		return fmt.Errorf("verify Redis projection: %w", err)
	}
	if count != int64(len(projected)) {
		return fmt.Errorf("%w: got %d, want %d", ErrProjectionCount, count, len(projected))
	}
	if err := s.advanceTo(ctx, job, ingestiondomain.StageVerifying); err != nil {
		return err
	}
	if err := s.advanceTo(ctx, job, ingestiondomain.StagePublishing); err != nil {
		return err
	}

	publishedAt := s.now()
	if err := s.documents.PublishVersion(ctx, ports.PublishVersionRequest{
		TenantID:        input.TenantID,
		KnowledgeBaseID: input.KnowledgeBaseID,
		DocumentID:      document.DocumentID(input.DocumentID),
		VersionID:       document.VersionID(input.VersionID),
		JobID:           job.ID,
		JobAttempt:      job.Attempt,
		LeaseOwner:      s.config.WorkerID,
		PublishedAt:     publishedAt,
		Artifacts:       artifacts,
	}); err != nil {
		return fmt.Errorf("publish document version: %w", err)
	}

	s.logger.InfoContext(ctx, "ingestion job published",
		"job_id", job.ID,
		"tenant_id", job.TenantID,
		"document_id", job.DocumentID,
		"version_id", job.VersionID,
		"chunks", len(result.Chunks),
		"tokens", result.TokenCount,
	)
	return nil
}

func (s *Service) persistArtifacts(
	ctx context.Context,
	input ports.VersionInput,
	result processing.Result,
) (ports.VersionArtifacts, error) {
	base := path.Join(
		s.config.ArtifactPrefix,
		input.TenantID,
		"knowledge-bases",
		input.KnowledgeBaseID,
		"documents",
		input.DocumentID,
		"versions",
		input.VersionID,
	)
	normalized, err := s.putArtifact(ctx, path.Join(base, "normalized", "document.json"), "application/json", "normalized", input, result.NormalizedJSON)
	if err != nil {
		return ports.VersionArtifacts{}, fmt.Errorf("store normalized document: %w", err)
	}
	manifest, err := s.putArtifact(ctx, path.Join(base, "chunks", "manifest.json"), "application/json", "chunk-manifest", input, result.ManifestJSON)
	if err != nil {
		_ = s.objects.Delete(context.WithoutCancel(ctx), normalized)
		return ports.VersionArtifacts{}, fmt.Errorf("store chunk manifest: %w", err)
	}
	return ports.VersionArtifacts{
		Original:   input.Original,
		Normalized: normalized,
		Manifest:   manifest,
		ChunkCount: int64(len(result.Chunks)),
		TokenCount: result.TokenCount,
	}, nil
}

func (s *Service) putArtifact(
	ctx context.Context,
	key string,
	mediaType string,
	kind string,
	input ports.VersionInput,
	content []byte,
) (ports.ObjectRef, error) {
	digest := sha256.Sum256(content)
	sha := hex.EncodeToString(digest[:])
	return s.objects.Put(ctx, ports.PutObject{
		Bucket:      s.config.Bucket,
		Key:         key,
		Body:        bytes.NewReader(content),
		Size:        int64(len(content)),
		ContentType: mediaType,
		Metadata: map[string]string{
			"content-sha256": sha,
			"artifact-kind":  kind,
			"document-id":    input.DocumentID,
			"version-id":     input.VersionID,
		},
	})
}

func (s *Service) advanceTo(ctx context.Context, job *ingestiondomain.Job, target ingestiondomain.Stage) error {
	if stageRank(job.Stage) > stageRank(target) {
		return nil
	}
	for job.Stage != target {
		next, ok := ingestiondomain.NextStage(job.Stage)
		if !ok {
			return fmt.Errorf("cannot advance job from %s to %s", job.Stage, target)
		}
		now := s.now()
		if err := job.Advance(s.config.WorkerID, next, now); err != nil {
			return err
		}
		expiresAt := now.Add(s.config.LeaseDuration)
		job.LeaseExpiresAt = &expiresAt
		if err := s.jobs.Save(ctx, *job); err != nil {
			return fmt.Errorf("save %s job stage: %w", next, err)
		}
	}
	return nil
}

func (s *Service) persistFailure(ctx context.Context, job *ingestiondomain.Job, cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return nil
	}
	now := s.now()
	code := processing.ErrorCode(cause)
	message := truncate(cause.Error(), 2000)
	terminal := processing.IsPermanent(cause) || job.Attempt >= job.MaxAttempts
	if terminal {
		markErr := s.documents.MarkVersionFailed(ctx, document.VersionID(job.VersionID), code, map[string]any{
			"job_id":  job.ID,
			"attempt": job.Attempt,
			"stage":   job.Stage,
			"error":   message,
		})
		if err := job.Fail(s.config.WorkerID, code, message, now); err != nil {
			return errors.Join(markErr, err)
		}
		if err := s.jobs.Save(ctx, *job); err != nil {
			return errors.Join(markErr, err)
		}
		return markErr
	}

	delay := s.retryDelay(job.Attempt)
	if err := job.Retry(s.config.WorkerID, now.Add(delay), code, message, now); err != nil {
		return err
	}
	if err := s.jobs.Save(ctx, *job); err != nil {
		return err
	}
	return nil
}

func (s *Service) retryDelay(attempt int) time.Duration {
	delay := s.config.RetryBase
	for count := 1; count < attempt && delay < s.config.RetryMaximum; count++ {
		if delay > s.config.RetryMaximum/2 {
			return s.config.RetryMaximum
		}
		delay *= 2
	}
	if delay > s.config.RetryMaximum {
		return s.config.RetryMaximum
	}
	return delay
}

func stageRank(stage ingestiondomain.Stage) int {
	order := []ingestiondomain.Stage{
		ingestiondomain.StageReceived,
		ingestiondomain.StageStored,
		ingestiondomain.StageValidating,
		ingestiondomain.StageParsing,
		ingestiondomain.StageNormalizing,
		ingestiondomain.StageChunking,
		ingestiondomain.StageEmbedding,
		ingestiondomain.StageIndexing,
		ingestiondomain.StageVerifying,
		ingestiondomain.StagePublishing,
		ingestiondomain.StageReady,
	}
	for index, candidate := range order {
		if stage == candidate {
			return index
		}
	}
	return -1
}

func truncate(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}
