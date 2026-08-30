package processing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/mss-boot-io/mss-knowledge/internal/adapters/embedding/deterministic"
	nativeparser "github.com/mss-boot-io/mss-knowledge/internal/adapters/parser/native"
	heuristictokenizer "github.com/mss-boot-io/mss-knowledge/internal/adapters/tokenizer/heuristic"
	"github.com/mss-boot-io/mss-knowledge/internal/app/chunking"
	ingestiondomain "github.com/mss-boot-io/mss-knowledge/internal/domain/ingestion"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

func TestPipelineBuildProducesDeterministicArtifactsAndStages(t *testing.T) {
	content := []byte("# MSS Knowledge\n\nRedis is the rebuildable context layer.\n\n```go\ntype Store interface { Check() error }\n```\n")
	store := newMemoryObjectStore("knowledge", "source.md", "source-v1", "text/markdown", content)
	pipeline, profiles := newTestPipeline(t, store)
	input := testVersionInput(store.reference, profiles)

	var stages []ingestiondomain.Stage
	result, err := pipeline.Build(t.Context(), input, func(_ context.Context, stage ingestiondomain.Stage) error {
		stages = append(stages, stage)
		return nil
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantStages := []ingestiondomain.Stage{
		ingestiondomain.StageStored,
		ingestiondomain.StageValidating,
		ingestiondomain.StageParsing,
		ingestiondomain.StageNormalizing,
		ingestiondomain.StageChunking,
		ingestiondomain.StageEmbedding,
	}
	if !reflect.DeepEqual(stages, wantStages) {
		t.Fatalf("stages = %v, want %v", stages, wantStages)
	}
	if result.Document.KnowledgeBaseID != input.KnowledgeBaseID || result.Document.Title != input.Title {
		t.Fatalf("document identity = %+v", result.Document)
	}
	if len(result.Chunks) == 0 || len(result.Embeddings) != len(result.Chunks) {
		t.Fatalf("chunks/embeddings = %d/%d", len(result.Chunks), len(result.Embeddings))
	}
	for index, vector := range result.Embeddings {
		if len(vector) != 32 {
			t.Fatalf("embedding %d dimension = %d", index, len(vector))
		}
	}
	var manifest ChunkManifest
	if err := json.Unmarshal(result.ManifestJSON, &manifest); err != nil {
		t.Fatalf("manifest JSON: %v", err)
	}
	if manifest.PipelineFingerprint != input.PipelineFingerprint || len(manifest.Chunks) != len(result.Chunks) {
		t.Fatalf("manifest = %+v", manifest)
	}

	second, err := pipeline.Build(t.Context(), input, nil)
	if err != nil {
		t.Fatalf("Build(second) error = %v", err)
	}
	if !bytes.Equal(result.NormalizedJSON, second.NormalizedJSON) ||
		!bytes.Equal(result.ManifestJSON, second.ManifestJSON) ||
		!reflect.DeepEqual(result.Chunks, second.Chunks) ||
		!reflect.DeepEqual(result.Embeddings, second.Embeddings) {
		t.Fatal("same immutable input produced different derived products")
	}
}

func TestPipelineBuildRejectsSourceIntegrityMismatchPermanently(t *testing.T) {
	content := []byte("# Integrity\n\nThe stored bytes must match metadata.\n")
	store := newMemoryObjectStore("knowledge", "source.md", "source-v1", "text/markdown", content)
	pipeline, profiles := newTestPipeline(t, store)
	input := testVersionInput(store.reference, profiles)
	store.reference.SHA256 = hex.EncodeToString(make([]byte, sha256.Size))
	input.Original.SHA256 = store.reference.SHA256

	_, err := pipeline.Build(t.Context(), input, nil)
	if err == nil {
		t.Fatal("Build() error = nil")
	}
	if !IsPermanent(err) || ErrorCode(err) != "SOURCE_INTEGRITY_MISMATCH" || !errors.Is(err, ErrSourceIntegrity) {
		t.Fatalf("Build() error = %v", err)
	}
}

func TestPipelineBuildRejectsMismatchedProfiles(t *testing.T) {
	content := []byte("# Profiles\n\nVersion profiles are immutable.\n")
	store := newMemoryObjectStore("knowledge", "source.md", "source-v1", "text/markdown", content)
	pipeline, profiles := newTestPipeline(t, store)
	input := testVersionInput(store.reference, profiles)
	input.Profiles.Index = "other-index"

	_, err := pipeline.Build(t.Context(), input, nil)
	if !IsPermanent(err) || ErrorCode(err) != "INVALID_VERSION_INPUT" {
		t.Fatalf("Build() error = %v", err)
	}
}

func newTestPipeline(t *testing.T, store ports.ObjectStore) (*Pipeline, ports.ProcessingProfileIDs) {
	t.Helper()
	parser, err := nativeparser.New(nativeparser.Config{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	chunker, err := chunking.NewStructural(heuristictokenizer.Counter{})
	if err != nil {
		t.Fatal(err)
	}
	profiles := ports.ProcessingProfileIDs{
		Parser: "parser-v1", Chunker: "chunker-v1", Embedding: "embedding-v1", Index: "index-v1",
	}
	pipeline, err := New(store, parser, chunker, deterministic.Provider{}, Config{
		MaxSourceBytes: 1 << 20,
		Profiles:       profiles,
		ChunkProfile: ports.ChunkProfile{
			Name: "structural", Version: "chunker-v1", TargetTokens: 24,
			MinimumTokens: 1, MaximumTokens: 64, OverlapTokens: 4,
			PreserveCode: true, PreserveTables: true,
		},
		EmbeddingProfile: ports.EmbeddingProfile{
			Provider: "deterministic", ModelID: "deterministic-v1", ModelRevision: "1",
			Dimension: 32, VectorType: "FLOAT32", Normalize: true, BatchSize: 2,
			Fingerprint: "embedding-fingerprint",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return pipeline, profiles
}

func testVersionInput(reference ports.ObjectRef, profiles ports.ProcessingProfileIDs) ports.VersionInput {
	return ports.VersionInput{
		TenantID: "tenant_1", KnowledgeBaseID: "kb_1", DocumentID: "doc_1", VersionID: "ver_1",
		Title: "MSS Knowledge", Filename: "source.md", MediaType: "text/markdown",
		DefaultLanguage: "chinese", SourceURI: "s3://knowledge/source.md?versionId=source-v1",
		Original: reference, Profiles: profiles, PipelineFingerprint: "pipeline-fingerprint",
	}
}

type memoryObjectStore struct {
	reference ports.ObjectRef
	content   []byte
	openErr   error
}

func newMemoryObjectStore(bucket, key, versionID, mediaType string, content []byte) *memoryObjectStore {
	digest := sha256.Sum256(content)
	return &memoryObjectStore{
		reference: ports.ObjectRef{
			Bucket: bucket, Key: key, VersionID: versionID, Size: int64(len(content)),
			SHA256: hex.EncodeToString(digest[:]), MediaType: mediaType,
		},
		content: append([]byte(nil), content...),
	}
}

func (s *memoryObjectStore) Put(context.Context, ports.PutObject) (ports.ObjectRef, error) {
	return ports.ObjectRef{}, errors.New("not implemented")
}
func (s *memoryObjectStore) Open(context.Context, ports.ObjectRef) (io.ReadCloser, error) {
	if s.openErr != nil {
		return nil, s.openErr
	}
	return io.NopCloser(bytes.NewReader(s.content)), nil
}
func (s *memoryObjectStore) Stat(context.Context, ports.ObjectRef) (ports.ObjectRef, error) {
	return s.reference, nil
}
func (s *memoryObjectStore) Delete(context.Context, ports.ObjectRef) error { return nil }
func (s *memoryObjectStore) Check(context.Context) error                   { return nil }
