package ports

import (
	"context"
	"io"
	"time"

	"github.com/mss-boot-io/mss-knowledge/internal/domain/document"
	"github.com/mss-boot-io/mss-knowledge/internal/domain/ingestion"
	"github.com/mss-boot-io/mss-knowledge/internal/domain/knowledge"
)

// ObjectRef identifies one immutable object and optional provider version.
type ObjectRef struct {
	Bucket    string
	Key       string
	VersionID string
	ETag      string
	Size      int64
	SHA256    string
	MediaType string
}

// PutObject describes an immutable object write.
type PutObject struct {
	Bucket      string
	Key         string
	Body        io.Reader
	Size        int64
	ContentType string
	Metadata    map[string]string
}

// ObjectStore persists original files, normalized documents, manifests, and assets.
type ObjectStore interface {
	Put(ctx context.Context, object PutObject) (ObjectRef, error)
	Open(ctx context.Context, reference ObjectRef) (io.ReadCloser, error)
	Stat(ctx context.Context, reference ObjectRef) (ObjectRef, error)
	Delete(ctx context.Context, reference ObjectRef) error
	Check(ctx context.Context) error
}

// ParseInput describes a validated source object opened by the worker. Parsers receive
// a bounded reader and source metadata; they never receive object-store credentials.
type ParseInput struct {
	Reference       ObjectRef
	Body            io.Reader
	Filename        string
	MediaType       string
	SourceType      string
	SourceURI       string
	KnowledgeBaseID string
	DocumentID      string
	VersionID       string
}

// Parser converts a validated object into the internal normalized document model.
type Parser interface {
	Name() string
	Supports(mediaType, filename string) bool
	Parse(ctx context.Context, input ParseInput) (knowledge.Document, error)
}

// ChunkProfile captures stable chunking parameters required by an implementation.
type ChunkProfile struct {
	Name            string
	Version         string
	TargetTokens    int
	MinimumTokens   int
	MaximumTokens   int
	OverlapTokens   int
	PreserveCode    bool
	PreserveTables  bool
	ParentExpansion bool
}

// TokenCounter abstracts model-specific token accounting from chunk structure logic.
type TokenCounter interface {
	Count(text string) int
}

// Chunker derives retrieval chunks from a normalized document.
type Chunker interface {
	Chunk(ctx context.Context, document knowledge.Document, profile ChunkProfile) ([]knowledge.Chunk, error)
}

// EmbeddingProfile identifies one reproducible model configuration.
type EmbeddingProfile struct {
	Provider         string
	ModelID          string
	ModelRevision    string
	Dimension        int
	VectorType       string
	Normalize        bool
	QueryInstruction string
	MaxInputTokens   int
	BatchSize        int
	Fingerprint      string
}

// EmbeddingProvider creates document and query embeddings.
type EmbeddingProvider interface {
	EmbedDocuments(ctx context.Context, texts []string, profile EmbeddingProfile) ([][]float32, error)
	EmbedQuery(ctx context.Context, query string, profile EmbeddingProfile) ([]float32, error)
	Check(ctx context.Context) error
}

// IndexedChunk combines durable chunk metadata with its derived vector.
type IndexedChunk struct {
	TenantID        string
	KnowledgeBaseID string
	DocumentID      string
	VersionID       string
	IndexVersion    string
	Chunk           knowledge.Chunk
	Language        string
	Title           string
	SourceURI       string
	UpdatedAt       time.Time
	Embedding       []float32
}

// ChunkIndex writes and verifies version-scoped search projections.
type ChunkIndex interface {
	IndexBatch(ctx context.Context, chunks []IndexedChunk) error
	CountVersion(ctx context.Context, tenantID, versionID, indexVersion string) (int64, error)
	DeleteVersion(ctx context.Context, tenantID, versionID, indexVersion string) error
	Check(ctx context.Context) error
}

// VersionArtifacts records durable S3 outputs before publication.
type VersionArtifacts struct {
	Original   ObjectRef
	Normalized ObjectRef
	Manifest   ObjectRef
	ChunkCount int64
	TokenCount int64
}

// PublishVersionRequest is the atomic control-plane visibility switch.
type PublishVersionRequest struct {
	TenantID        string
	KnowledgeBaseID string
	DocumentID      document.DocumentID
	VersionID       document.VersionID
	JobID           ingestion.JobID
	JobAttempt      int
	LeaseOwner      string
	PublishedAt     time.Time
	Artifacts       VersionArtifacts
}

// DocumentRepository persists version metadata and performs publication transactions.
type DocumentRepository interface {
	SaveArtifacts(ctx context.Context, versionID document.VersionID, artifacts VersionArtifacts) error
	PublishVersion(ctx context.Context, request PublishVersionRequest) error
	MarkVersionFailed(ctx context.Context, versionID document.VersionID, code string, detail map[string]any) error
	ActiveVersionIDs(ctx context.Context, tenantID string, documentIDs []string) (map[string]string, error)
}

// JobRepository leases and persists ingestion jobs.
type JobRepository interface {
	ClaimNext(ctx context.Context, workerID string, now time.Time, leaseDuration time.Duration) (*ingestion.Job, error)
	Save(ctx context.Context, job ingestion.Job) error
	Check(ctx context.Context) error
}

// ProcessingProfileIDs selects the immutable processing configuration for an upload.
type ProcessingProfileIDs struct {
	Parser    string
	Chunker   string
	Embedding string
	Index     string
}

// CreateUploadRequest records an immutable source object and schedules its ingestion.
type CreateUploadRequest struct {
	TenantID            string
	PrincipalID         string
	KnowledgeBaseID     string
	DocumentID          string
	VersionID           string
	JobID               string
	ExternalKey         string
	Title               string
	Filename            string
	MediaType           string
	SourceURI           string
	Original            ObjectRef
	Profiles            ProcessingProfileIDs
	PipelineFingerprint string
	CreatedAt           time.Time
}

// CreateUploadResult reports the durable identifiers assigned to an accepted upload.
type CreateUploadResult struct {
	DocumentID    string
	VersionID     string
	JobID         string
	VersionNumber int64
}

// VersionInput is the durable source and processing configuration loaded by a worker.
type VersionInput struct {
	TenantID            string
	KnowledgeBaseID     string
	DocumentID          string
	VersionID           string
	Title               string
	Filename            string
	MediaType           string
	DefaultLanguage     string
	SourceURI           string
	Original            ObjectRef
	Profiles            ProcessingProfileIDs
	PipelineFingerprint string
}

// StoredChunk is a PostgreSQL chunk-catalog record. The text remains in the S3 manifest.
type StoredChunk struct {
	Chunk         knowledge.Chunk
	TextObjectKey string
}

// UploadRepository creates the control-plane records for one already-stored source object.
type UploadRepository interface {
	CreateUpload(ctx context.Context, request CreateUploadRequest) (CreateUploadResult, error)
}

// IngestionReader loads tenant-scoped work inputs and job state.
type IngestionReader interface {
	GetJob(ctx context.Context, tenantID string, jobID ingestion.JobID) (ingestion.Job, error)
	LoadVersionInput(ctx context.Context, tenantID string, versionID string) (VersionInput, error)
}

// ActiveVersionInput identifies one currently visible version that can be rebuilt from S3.
type ActiveVersionInput struct {
	VersionInput
	PublishedAt time.Time
}

// RebuildRepository enumerates published versions for Redis reconstruction.
type RebuildRepository interface {
	ListActiveVersionInputs(ctx context.Context, tenantID, knowledgeBaseID string) ([]ActiveVersionInput, error)
}

// ChunkRepository replaces the durable chunk catalog for one immutable version.
type ChunkRepository interface {
	ReplaceVersionChunks(ctx context.Context, tenantID, versionID string, chunks []StoredChunk) error
}

// KnowledgeBaseWriteAuthorizer checks whether a principal may add documents to a knowledge base.
type KnowledgeBaseWriteAuthorizer interface {
	CanWriteKnowledgeBase(ctx context.Context, tenantID, principalID, knowledgeBaseID string) (bool, error)
}
