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
	Reference  ObjectRef
	Body       io.Reader
	Filename   string
	MediaType  string
	SourceType string
	SourceURI  string
	DocumentID string
	VersionID  string
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
	PublishedAt     time.Time
	Artifacts       VersionArtifacts
}

// DocumentRepository persists version metadata and performs publication transactions.
type DocumentRepository interface {
	CreateVersion(ctx context.Context, version document.Version) error
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
