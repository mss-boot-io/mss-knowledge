package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/mss-boot-io/mss-knowledge/internal/adapters/embedding/deterministic"
	nativeparser "github.com/mss-boot-io/mss-knowledge/internal/adapters/parser/native"
	heuristictokenizer "github.com/mss-boot-io/mss-knowledge/internal/adapters/tokenizer/heuristic"
	"github.com/mss-boot-io/mss-knowledge/internal/app/chunking"
	"github.com/mss-boot-io/mss-knowledge/internal/app/processing"
	"github.com/mss-boot-io/mss-knowledge/internal/domain/document"
	ingestiondomain "github.com/mss-boot-io/mss-knowledge/internal/domain/ingestion"
	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

func TestServiceRunOncePublishesCompleteProjection(t *testing.T) {
	service, repository, objects, index := newTestWorker(t)

	processed, err := service.RunOnce(t.Context())
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if !processed {
		t.Fatal("RunOnce() processed = false")
	}
	if repository.published == nil {
		t.Fatal("document version was not published")
	}
	if repository.published.JobAttempt != 1 || repository.published.LeaseOwner != "worker-test" {
		t.Fatalf("publication fence = %+v", repository.published)
	}
	if repository.artifacts.ChunkCount == 0 || len(repository.chunks) == 0 {
		t.Fatalf("artifacts/chunks = %+v / %d", repository.artifacts, len(repository.chunks))
	}
	if repository.artifacts.ChunkCount != int64(len(repository.chunks)) {
		t.Fatalf("chunk count = %d, catalog = %d", repository.artifacts.ChunkCount, len(repository.chunks))
	}
	if len(index.chunks) != len(repository.chunks) {
		t.Fatalf("Redis projection = %d, durable chunks = %d", len(index.chunks), len(repository.chunks))
	}
	if len(objects.puts) != 2 {
		t.Fatalf("artifact puts = %d, want 2", len(objects.puts))
	}
	if !reflect.DeepEqual(repository.savedStages, []ingestiondomain.Stage{
		ingestiondomain.StageStored,
		ingestiondomain.StageValidating,
		ingestiondomain.StageParsing,
		ingestiondomain.StageNormalizing,
		ingestiondomain.StageChunking,
		ingestiondomain.StageEmbedding,
		ingestiondomain.StageIndexing,
		ingestiondomain.StageVerifying,
		ingestiondomain.StagePublishing,
	}) {
		t.Fatalf("saved stages = %v", repository.savedStages)
	}
}

func TestServiceRunOnceMarksPermanentFailure(t *testing.T) {
	service, repository, _, _ := newTestWorker(t)
	repository.input.MediaType = "application/pdf"
	repository.input.Filename = "unsupported.pdf"

	processed, err := service.RunOnce(t.Context())
	if !processed || err == nil {
		t.Fatalf("RunOnce() = %v, %v", processed, err)
	}
	if repository.failedCode != "UNSUPPORTED_MEDIA_TYPE" {
		t.Fatalf("failed code = %q", repository.failedCode)
	}
	if repository.job.State != ingestiondomain.StateFailed || repository.job.ErrorCode != "UNSUPPORTED_MEDIA_TYPE" {
		t.Fatalf("job = %+v", repository.job)
	}
}

func TestServiceRunOnceSchedulesRetryForTransientFailure(t *testing.T) {
	service, repository, objects, _ := newTestWorker(t)
	objects.openErr = errors.New("temporary S3 transport failure")
	base := service.now()

	processed, err := service.RunOnce(t.Context())
	if !processed || err == nil {
		t.Fatalf("RunOnce() = %v, %v", processed, err)
	}
	if repository.job.State != ingestiondomain.StateRetryWait {
		t.Fatalf("job state = %s", repository.job.State)
	}
	if repository.job.ErrorCode != "PROCESSING_FAILED" || !repository.job.NextAttemptAt.Equal(base.Add(time.Second)) {
		t.Fatalf("retry job = %+v", repository.job)
	}
	if repository.failedCode != "" {
		t.Fatalf("version was terminally failed with %q", repository.failedCode)
	}
}

func newTestWorker(t *testing.T) (*Service, *fakeRepository, *fakeObjectStore, *fakeChunkIndex) {
	t.Helper()
	base := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	content := []byte("# Worker verification\n\nThe worker publishes only complete version projections.\n")
	digest := sha256.Sum256(content)
	original := ports.ObjectRef{
		Bucket: "knowledge", Key: "tenants/tenant_1/source.md", VersionID: "source-v1",
		Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:]), MediaType: "text/markdown",
	}
	profiles := ports.ProcessingProfileIDs{
		Parser: "parser-v1", Chunker: "chunker-v1", Embedding: "embedding-v1", Index: "index-v1",
	}
	job, err := ingestiondomain.NewJob(
		"job_1", "tenant_1", "kb_1", "doc_1", "ver_1", "ingest", 3, base.Add(-time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	repository := &fakeRepository{
		job: job,
		input: ports.VersionInput{
			TenantID: "tenant_1", KnowledgeBaseID: "kb_1", DocumentID: "doc_1", VersionID: "ver_1",
			Title: "Worker verification", Filename: "source.md", MediaType: "text/markdown",
			DefaultLanguage: "chinese", SourceURI: "s3://knowledge/source.md?versionId=source-v1",
			Original: original, Profiles: profiles, PipelineFingerprint: "pipeline-fingerprint",
		},
	}
	objects := &fakeObjectStore{original: original, source: content}
	index := &fakeChunkIndex{}
	parser, err := nativeparser.New(nativeparser.Config{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	chunker, err := chunking.NewStructural(heuristictokenizer.Counter{})
	if err != nil {
		t.Fatal(err)
	}
	embeddingProfile := ports.EmbeddingProfile{
		Provider: "deterministic", ModelID: "deterministic-v1", ModelRevision: "1",
		Dimension: 32, VectorType: "FLOAT32", Normalize: true, BatchSize: 8,
		Fingerprint: "embedding-fingerprint",
	}
	pipeline, err := processing.New(objects, parser, chunker, deterministic.Provider{}, processing.Config{
		MaxSourceBytes: 1 << 20,
		Profiles:       profiles,
		ChunkProfile: ports.ChunkProfile{
			Name: "structural", Version: "chunker-v1", TargetTokens: 24,
			MinimumTokens: 1, MaximumTokens: 64, OverlapTokens: 4,
			PreserveCode: true, PreserveTables: true,
		},
		EmbeddingProfile: embeddingProfile,
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(
		repository, repository, objects, repository, repository, index, pipeline,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config{
			WorkerID: "worker-test", PollInterval: time.Millisecond, LeaseDuration: time.Minute,
			RetryBase: time.Second, RetryMaximum: 8 * time.Second,
			ArtifactPrefix: "tenants", Bucket: "knowledge", IndexVersion: "index-v1",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return base }
	return service, repository, objects, index
}

type fakeRepository struct {
	mu          sync.Mutex
	job         ingestiondomain.Job
	input       ports.VersionInput
	chunks      []ports.StoredChunk
	artifacts   ports.VersionArtifacts
	published   *ports.PublishVersionRequest
	failedCode  string
	savedStages []ingestiondomain.Stage
	claimed     bool
}

func (r *fakeRepository) ClaimNext(_ context.Context, workerID string, now time.Time, lease time.Duration) (*ingestiondomain.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimed {
		return nil, nil
	}
	r.claimed = true
	if err := r.job.Claim(workerID, now, now.Add(lease)); err != nil {
		return nil, err
	}
	copy := r.job
	return &copy, nil
}

func (r *fakeRepository) Save(_ context.Context, job ingestiondomain.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.job = job
	r.savedStages = append(r.savedStages, job.Stage)
	return nil
}
func (r *fakeRepository) Check(context.Context) error { return nil }
func (r *fakeRepository) GetJob(_ context.Context, tenantID string, id ingestiondomain.JobID) (ingestiondomain.Job, error) {
	if r.job.TenantID != tenantID || r.job.ID != id {
		return ingestiondomain.Job{}, ports.ErrNotFound
	}
	return r.job, nil
}
func (r *fakeRepository) LoadVersionInput(_ context.Context, tenantID, versionID string) (ports.VersionInput, error) {
	if r.input.TenantID != tenantID || r.input.VersionID != versionID {
		return ports.VersionInput{}, ports.ErrNotFound
	}
	return r.input, nil
}
func (r *fakeRepository) ReplaceVersionChunks(_ context.Context, tenantID, versionID string, chunks []ports.StoredChunk) error {
	if tenantID != r.input.TenantID || versionID != r.input.VersionID {
		return ports.ErrNotFound
	}
	r.chunks = append([]ports.StoredChunk(nil), chunks...)
	return nil
}
func (r *fakeRepository) SaveArtifacts(_ context.Context, versionID document.VersionID, artifacts ports.VersionArtifacts) error {
	if string(versionID) != r.input.VersionID {
		return ports.ErrNotFound
	}
	r.artifacts = artifacts
	return nil
}
func (r *fakeRepository) PublishVersion(_ context.Context, request ports.PublishVersionRequest) error {
	copy := request
	r.published = &copy
	return nil
}
func (r *fakeRepository) MarkVersionFailed(_ context.Context, _ document.VersionID, code string, _ map[string]any) error {
	r.failedCode = code
	return nil
}
func (r *fakeRepository) ActiveVersionIDs(context.Context, string, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *fakeRepository) AllowedKnowledgeBases(context.Context, searchdomain.Principal, []string) ([]string, error) {
	return nil, nil
}
func (r *fakeRepository) CanReadHit(context.Context, searchdomain.Principal, searchdomain.Hit) (bool, error) {
	return false, nil
}

type fakeObjectStore struct {
	mu       sync.Mutex
	original ports.ObjectRef
	source   []byte
	openErr  error
	puts     []ports.ObjectRef
}

func (s *fakeObjectStore) Put(_ context.Context, object ports.PutObject) (ports.ObjectRef, error) {
	content, err := io.ReadAll(object.Body)
	if err != nil {
		return ports.ObjectRef{}, err
	}
	digest := sha256.Sum256(content)
	reference := ports.ObjectRef{
		Bucket: object.Bucket, Key: object.Key, VersionID: "artifact-v" + string(rune('1'+len(s.puts))),
		Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:]), MediaType: object.ContentType,
	}
	s.mu.Lock()
	s.puts = append(s.puts, reference)
	s.mu.Unlock()
	return reference, nil
}
func (s *fakeObjectStore) Open(_ context.Context, reference ports.ObjectRef) (io.ReadCloser, error) {
	if s.openErr != nil {
		return nil, s.openErr
	}
	if reference.Key != s.original.Key || reference.VersionID != s.original.VersionID {
		return nil, ports.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(s.source)), nil
}
func (s *fakeObjectStore) Stat(_ context.Context, reference ports.ObjectRef) (ports.ObjectRef, error) {
	if reference.Key != s.original.Key || reference.VersionID != s.original.VersionID {
		return ports.ObjectRef{}, ports.ErrNotFound
	}
	return s.original, nil
}
func (s *fakeObjectStore) Delete(context.Context, ports.ObjectRef) error { return nil }
func (s *fakeObjectStore) Check(context.Context) error                   { return nil }

type fakeChunkIndex struct {
	chunks []ports.IndexedChunk
}

func (i *fakeChunkIndex) IndexBatch(_ context.Context, chunks []ports.IndexedChunk) error {
	i.chunks = append([]ports.IndexedChunk(nil), chunks...)
	return nil
}
func (i *fakeChunkIndex) CountVersion(_ context.Context, tenantID, versionID, indexVersion string) (int64, error) {
	var count int64
	for _, chunk := range i.chunks {
		if chunk.TenantID == tenantID && chunk.VersionID == versionID && chunk.IndexVersion == indexVersion {
			count++
		}
	}
	return count, nil
}
func (i *fakeChunkIndex) DeleteVersion(_ context.Context, tenantID, versionID, indexVersion string) error {
	filtered := i.chunks[:0]
	for _, chunk := range i.chunks {
		if chunk.TenantID == tenantID && chunk.VersionID == versionID && chunk.IndexVersion == indexVersion {
			continue
		}
		filtered = append(filtered, chunk)
	}
	i.chunks = filtered
	return nil
}
func (i *fakeChunkIndex) Check(context.Context) error { return nil }
