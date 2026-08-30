package processing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	ingestiondomain "github.com/mss-boot-io/mss-knowledge/internal/domain/ingestion"
	"github.com/mss-boot-io/mss-knowledge/internal/domain/knowledge"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

var (
	// ErrInvalidDependencies is returned when a required processing adapter is absent.
	ErrInvalidDependencies = errors.New("invalid processing dependencies")
	// ErrInvalidConfig is returned when processing limits or profiles are unsafe.
	ErrInvalidConfig = errors.New("invalid processing configuration")
	// ErrInvalidInput is returned when a durable version cannot be processed safely.
	ErrInvalidInput = errors.New("invalid processing input")
	// ErrSourceIntegrity is returned when immutable source bytes do not match PostgreSQL metadata.
	ErrSourceIntegrity = errors.New("source object integrity mismatch")
	// ErrUnsupportedSource is returned when no configured parser accepts the source.
	ErrUnsupportedSource = errors.New("unsupported source document")
	// ErrInvalidResult is returned when a provider produces inconsistent output.
	ErrInvalidResult = errors.New("invalid processing result")
)

// PermanentError marks a failure that cannot be repaired by retrying the same immutable input.
type PermanentError struct {
	Code string
	Err  error
}

func (e *PermanentError) Error() string {
	if e == nil {
		return "permanent processing failure"
	}
	if strings.TrimSpace(e.Code) == "" {
		return e.Err.Error()
	}
	return e.Code + ": " + e.Err.Error()
}

func (e *PermanentError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsPermanent reports whether retrying the same immutable input cannot succeed.
func IsPermanent(err error) bool {
	var target *PermanentError
	return errors.As(err, &target)
}

// ErrorCode returns a stable failure code suitable for durable job state.
func ErrorCode(err error) string {
	var target *PermanentError
	if errors.As(err, &target) && strings.TrimSpace(target.Code) != "" {
		return target.Code
	}
	return "PROCESSING_FAILED"
}

// Checkpoint is invoked after one deterministic processing stage has completed.
type Checkpoint func(context.Context, ingestiondomain.Stage) error

// Config selects the immutable processing profiles and hard input limits.
type Config struct {
	MaxSourceBytes   int64
	Profiles         ports.ProcessingProfileIDs
	ChunkProfile     ports.ChunkProfile
	EmbeddingProfile ports.EmbeddingProfile
}

// Pipeline rebuilds normalized documents, chunks, and embeddings from immutable S3 input.
type Pipeline struct {
	objects  ports.ObjectStore
	parser   ports.Parser
	chunker  ports.Chunker
	embedder ports.EmbeddingProvider
	config   Config
}

// Result contains deterministic products used by both ingestion and Redis rebuilds.
type Result struct {
	Document       knowledge.Document
	Chunks         []knowledge.Chunk
	Embeddings     [][]float32
	NormalizedJSON []byte
	ManifestJSON   []byte
	TokenCount     int64
}

// ChunkManifest is the durable, parser-independent rebuild source stored in S3.
type ChunkManifest struct {
	SchemaVersion       string                     `json:"schema_version"`
	TenantID            string                     `json:"tenant_id"`
	KnowledgeBaseID     string                     `json:"knowledge_base_id"`
	DocumentID          string                     `json:"document_id"`
	VersionID           string                     `json:"version_id"`
	Title               string                     `json:"title"`
	Language            string                     `json:"language,omitempty"`
	SourceURI           string                     `json:"source_uri"`
	PipelineFingerprint string                     `json:"pipeline_fingerprint"`
	Profiles            ports.ProcessingProfileIDs `json:"profiles"`
	Chunks              []knowledge.Chunk          `json:"chunks"`
}

// New creates a deterministic document-processing pipeline.
func New(
	objects ports.ObjectStore,
	parser ports.Parser,
	chunker ports.Chunker,
	embedder ports.EmbeddingProvider,
	config Config,
) (*Pipeline, error) {
	if objects == nil || parser == nil || chunker == nil || embedder == nil {
		return nil, ErrInvalidDependencies
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	return &Pipeline{
		objects:  objects,
		parser:   parser,
		chunker:  chunker,
		embedder: embedder,
		config:   config,
	}, nil
}

// Build reconstructs every derived product from the immutable original object.
func (p *Pipeline) Build(
	ctx context.Context,
	input ports.VersionInput,
	checkpoint Checkpoint,
) (Result, error) {
	if p == nil {
		return Result{}, ErrInvalidDependencies
	}
	if err := validateInput(input, p.config.Profiles); err != nil {
		return Result{}, permanent("INVALID_VERSION_INPUT", err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	reference, err := p.objects.Stat(ctx, input.Original)
	if err != nil {
		return Result{}, fmt.Errorf("stat immutable source: %w", err)
	}
	if err := runCheckpoint(ctx, checkpoint, ingestiondomain.StageStored); err != nil {
		return Result{}, err
	}
	if reference.Size > p.config.MaxSourceBytes {
		return Result{}, permanent("SOURCE_TOO_LARGE", fmt.Errorf("%w: %d bytes exceeds %d", ErrInvalidInput, reference.Size, p.config.MaxSourceBytes))
	}

	source, err := p.readAndVerify(ctx, reference)
	if err != nil {
		return Result{}, err
	}
	if err := runCheckpoint(ctx, checkpoint, ingestiondomain.StageValidating); err != nil {
		return Result{}, err
	}
	if !p.parser.Supports(input.MediaType, input.Filename) {
		return Result{}, permanent("UNSUPPORTED_MEDIA_TYPE", fmt.Errorf("%w: %s (%s)", ErrUnsupportedSource, input.Filename, input.MediaType))
	}

	document, err := p.parser.Parse(ctx, ports.ParseInput{
		Reference:       reference,
		Body:            bytes.NewReader(source),
		Filename:        input.Filename,
		MediaType:       input.MediaType,
		SourceType:      "s3",
		SourceURI:       input.SourceURI,
		KnowledgeBaseID: input.KnowledgeBaseID,
		DocumentID:      input.DocumentID,
		VersionID:       input.VersionID,
	})
	if err != nil {
		return Result{}, permanent("PARSE_FAILED", fmt.Errorf("parse immutable source: %w", err))
	}
	if err := runCheckpoint(ctx, checkpoint, ingestiondomain.StageParsing); err != nil {
		return Result{}, err
	}

	if title := strings.TrimSpace(input.Title); title != "" {
		document.Title = title
	}
	if strings.TrimSpace(document.Language) == "" {
		document.Language = strings.TrimSpace(input.DefaultLanguage)
	}
	if err := document.Validate(); err != nil {
		return Result{}, permanent("NORMALIZATION_FAILED", fmt.Errorf("validate normalized document: %w", err))
	}
	normalized, err := marshalLine(document)
	if err != nil {
		return Result{}, permanent("NORMALIZATION_FAILED", fmt.Errorf("encode normalized document: %w", err))
	}
	if err := runCheckpoint(ctx, checkpoint, ingestiondomain.StageNormalizing); err != nil {
		return Result{}, err
	}

	chunks, err := p.chunker.Chunk(ctx, document, p.config.ChunkProfile)
	if err != nil {
		return Result{}, permanent("CHUNKING_FAILED", fmt.Errorf("chunk normalized document: %w", err))
	}
	if len(chunks) == 0 {
		return Result{}, permanent("CHUNKING_FAILED", fmt.Errorf("%w: no chunks produced", ErrInvalidResult))
	}
	if err := runCheckpoint(ctx, checkpoint, ingestiondomain.StageChunking); err != nil {
		return Result{}, err
	}

	texts := make([]string, len(chunks))
	var tokenCount int64
	for index, chunk := range chunks {
		texts[index] = chunk.Text
		tokenCount += int64(chunk.TokenCount)
	}
	embeddings, err := p.embedDocuments(ctx, texts)
	if err != nil {
		return Result{}, fmt.Errorf("embed document chunks: %w", err)
	}
	if err := runCheckpoint(ctx, checkpoint, ingestiondomain.StageEmbedding); err != nil {
		return Result{}, err
	}

	manifest, err := marshalLine(ChunkManifest{
		SchemaVersion:       "1.0",
		TenantID:            input.TenantID,
		KnowledgeBaseID:     input.KnowledgeBaseID,
		DocumentID:          input.DocumentID,
		VersionID:           input.VersionID,
		Title:               document.Title,
		Language:            document.Language,
		SourceURI:           input.SourceURI,
		PipelineFingerprint: input.PipelineFingerprint,
		Profiles:            input.Profiles,
		Chunks:              chunks,
	})
	if err != nil {
		return Result{}, permanent("MANIFEST_FAILED", fmt.Errorf("encode chunk manifest: %w", err))
	}
	return Result{
		Document:       document,
		Chunks:         chunks,
		Embeddings:     embeddings,
		NormalizedJSON: normalized,
		ManifestJSON:   manifest,
		TokenCount:     tokenCount,
	}, nil
}

func (p *Pipeline) readAndVerify(ctx context.Context, reference ports.ObjectRef) ([]byte, error) {
	reader, err := p.objects.Open(ctx, reference)
	if err != nil {
		return nil, fmt.Errorf("open immutable source: %w", err)
	}
	defer reader.Close()

	hasher := sha256.New()
	limited := io.LimitReader(reader, p.config.MaxSourceBytes+1)
	content, err := io.ReadAll(io.TeeReader(limited, hasher))
	if err != nil {
		return nil, fmt.Errorf("read immutable source: %w", err)
	}
	if int64(len(content)) > p.config.MaxSourceBytes {
		return nil, permanent("SOURCE_TOO_LARGE", fmt.Errorf("%w: source exceeds %d bytes", ErrInvalidInput, p.config.MaxSourceBytes))
	}
	actualSHA := hex.EncodeToString(hasher.Sum(nil))
	if reference.Size >= 0 && int64(len(content)) != reference.Size {
		return nil, permanent("SOURCE_INTEGRITY_MISMATCH", fmt.Errorf("%w: expected %d bytes, read %d", ErrSourceIntegrity, reference.Size, len(content)))
	}
	if expected := strings.ToLower(strings.TrimSpace(reference.SHA256)); expected == "" || expected != actualSHA {
		return nil, permanent("SOURCE_INTEGRITY_MISMATCH", fmt.Errorf("%w: expected %s, read %s", ErrSourceIntegrity, expected, actualSHA))
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return nil, permanent("EMPTY_SOURCE", fmt.Errorf("%w: source is empty", ErrInvalidInput))
	}
	return content, nil
}

func (p *Pipeline) embedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	batchSize := p.config.EmbeddingProfile.BatchSize
	if batchSize <= 0 || batchSize > len(texts) {
		batchSize = len(texts)
	}
	result := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += batchSize {
		end := start + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		vectors, err := p.embedder.EmbedDocuments(ctx, texts[start:end], p.config.EmbeddingProfile)
		if err != nil {
			return nil, err
		}
		if len(vectors) != end-start {
			return nil, fmt.Errorf("%w: provider returned %d vectors for %d texts", ErrInvalidResult, len(vectors), end-start)
		}
		for index, vector := range vectors {
			if len(vector) != p.config.EmbeddingProfile.Dimension {
				return nil, fmt.Errorf("%w: vector %d dimension is %d, want %d", ErrInvalidResult, start+index, len(vector), p.config.EmbeddingProfile.Dimension)
			}
			for _, value := range vector {
				if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
					return nil, fmt.Errorf("%w: vector %d contains a non-finite value", ErrInvalidResult, start+index)
				}
			}
		}
		result = append(result, vectors...)
	}
	return result, nil
}

func validateConfig(config Config) error {
	if config.MaxSourceBytes <= 0 {
		return fmt.Errorf("%w: max source bytes must be positive", ErrInvalidConfig)
	}
	for _, value := range []string{config.Profiles.Parser, config.Profiles.Chunker, config.Profiles.Embedding, config.Profiles.Index} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: processing profile IDs must not be empty", ErrInvalidConfig)
		}
	}
	if strings.TrimSpace(config.ChunkProfile.Name) == "" || strings.TrimSpace(config.ChunkProfile.Version) == "" {
		return fmt.Errorf("%w: chunk profile identity is required", ErrInvalidConfig)
	}
	if config.EmbeddingProfile.Dimension < 16 || strings.TrimSpace(config.EmbeddingProfile.ModelID) == "" {
		return fmt.Errorf("%w: embedding profile is invalid", ErrInvalidConfig)
	}
	return nil
}

func validateInput(input ports.VersionInput, expected ports.ProcessingProfileIDs) error {
	values := []string{
		input.TenantID,
		input.KnowledgeBaseID,
		input.DocumentID,
		input.VersionID,
		input.Title,
		input.Filename,
		input.MediaType,
		input.SourceURI,
		input.Original.Bucket,
		input.Original.Key,
		input.Original.VersionID,
		input.Original.SHA256,
		input.PipelineFingerprint,
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: required version field is empty", ErrInvalidInput)
		}
	}
	if input.Original.Size <= 0 {
		return fmt.Errorf("%w: source size must be positive", ErrInvalidInput)
	}
	if input.Profiles != expected {
		return fmt.Errorf("%w: version processing profiles do not match this worker", ErrInvalidInput)
	}
	return nil
}

func runCheckpoint(ctx context.Context, checkpoint Checkpoint, stage ingestiondomain.Stage) error {
	if checkpoint == nil {
		return nil
	}
	if err := checkpoint(ctx, stage); err != nil {
		return fmt.Errorf("save %s checkpoint: %w", stage, err)
	}
	return nil
}

func permanent(code string, err error) error {
	return &PermanentError{Code: code, Err: err}
}

func marshalLine(value any) ([]byte, error) {
	content, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}
