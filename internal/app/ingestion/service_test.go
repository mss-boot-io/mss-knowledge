package ingestion

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	ingestiondomain "github.com/mss-boot-io/mss-knowledge/internal/domain/ingestion"
	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

type idsStub struct{ next int }

func (s *idsStub) New(prefix string) (string, error) {
	s.next++
	return prefix + "_id", nil
}

type authorizerStub struct{ allowed bool }

func (s authorizerStub) CanWriteKnowledgeBase(context.Context, string, string, string) (bool, error) {
	return s.allowed, nil
}
func (s authorizerStub) AllowedKnowledgeBases(_ context.Context, _ searchdomain.Principal, requested []string) ([]string, error) {
	if !s.allowed {
		return nil, nil
	}
	return append([]string(nil), requested...), nil
}
func (s authorizerStub) CanReadHit(context.Context, searchdomain.Principal, searchdomain.Hit) (bool, error) {
	return s.allowed, nil
}

type objectStoreStub struct {
	put     ports.PutObject
	deleted bool
}

func (s *objectStoreStub) Put(_ context.Context, object ports.PutObject) (ports.ObjectRef, error) {
	content, _ := io.ReadAll(object.Body)
	s.put = object
	return ports.ObjectRef{Bucket: "knowledge", Key: object.Key, VersionID: "version-1", Size: int64(len(content)), SHA256: strings.Repeat("a", 64), MediaType: object.ContentType}, nil
}
func (s *objectStoreStub) Open(context.Context, ports.ObjectRef) (io.ReadCloser, error) {
	return nil, nil
}
func (s *objectStoreStub) Stat(context.Context, ports.ObjectRef) (ports.ObjectRef, error) {
	return ports.ObjectRef{}, nil
}
func (s *objectStoreStub) Delete(context.Context, ports.ObjectRef) error {
	s.deleted = true
	return nil
}
func (s *objectStoreStub) Check(context.Context) error { return nil }

type uploadRepoStub struct{ request ports.CreateUploadRequest }

func (s *uploadRepoStub) CreateUpload(_ context.Context, request ports.CreateUploadRequest) (ports.CreateUploadResult, error) {
	s.request = request
	return ports.CreateUploadResult{DocumentID: request.DocumentID, VersionID: request.VersionID, JobID: request.JobID, VersionNumber: 1}, nil
}

type ingestionReaderStub struct{ job ingestiondomain.Job }

func (s ingestionReaderStub) GetJob(context.Context, string, ingestiondomain.JobID) (ingestiondomain.Job, error) {
	return s.job, nil
}
func (s ingestionReaderStub) LoadVersionInput(context.Context, string, string) (ports.VersionInput, error) {
	return ports.VersionInput{}, nil
}

func TestSubmitStoresAndSchedulesDocument(t *testing.T) {
	objects := &objectStoreStub{}
	uploads := &uploadRepoStub{}
	service, err := New(
		authorizerStub{allowed: true},
		authorizerStub{allowed: true},
		objects,
		uploads,
		ingestionReaderStub{},
		&idsStub{},
		Config{
			Bucket:             "knowledge",
			MaxBytes:           1024,
			Profiles:           ports.ProcessingProfileIDs{Parser: "parser", Chunker: "chunker", Embedding: "embedding", Index: "index"},
			EmbeddingModel:     "test",
			EmbeddingDimension: 128,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	principal := searchdomain.Principal{
		TenantID: "tenant_1", PrincipalID: "principal_1",
		Scopes: map[string]struct{}{ScopeKnowledgeWrite: {}},
	}
	result, err := service.Submit(t.Context(), principal, SubmitRequest{
		KnowledgeBaseID: "kb_1",
		Filename:        "design.md",
		MediaType:       "text/markdown; charset=utf-8",
		Body:            bytes.NewBufferString("# Design\n\nRedis is the context layer."),
	})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if result.JobID != "job_id" || result.State != string(ingestiondomain.StatePending) {
		t.Fatalf("result = %+v", result)
	}
	if uploads.request.Original.VersionID != "version-1" || uploads.request.PipelineFingerprint == "" {
		t.Fatalf("upload request = %+v", uploads.request)
	}
	if objects.put.Bucket != "knowledge" {
		t.Fatalf("object bucket = %q", objects.put.Bucket)
	}
	if !strings.Contains(objects.put.Key, "/raw/design.md") {
		t.Fatalf("object key = %q", objects.put.Key)
	}
}

func TestSubmitRejectsUnsupportedDocument(t *testing.T) {
	service, _ := New(
		authorizerStub{allowed: true},
		authorizerStub{allowed: true},
		&objectStoreStub{},
		&uploadRepoStub{},
		ingestionReaderStub{},
		&idsStub{},
		Config{Bucket: "knowledge", MaxBytes: 1024, Profiles: ports.ProcessingProfileIDs{Parser: "p", Chunker: "c", Embedding: "e", Index: "i"}, EmbeddingDimension: 128},
	)
	principal := searchdomain.Principal{TenantID: "tenant_1", PrincipalID: "principal_1", Scopes: map[string]struct{}{ScopeKnowledgeWrite: {}}}
	_, err := service.Submit(t.Context(), principal, SubmitRequest{KnowledgeBaseID: "kb_1", Filename: "file.pdf", MediaType: "application/pdf", Body: bytes.NewBufferString("pdf")})
	if err == nil {
		t.Fatal("Submit() error = nil")
	}
}

func TestStatusReturnsTenantScopedJob(t *testing.T) {
	now := time.Now().UTC()
	service, _ := New(
		authorizerStub{allowed: true},
		authorizerStub{allowed: true},
		&objectStoreStub{},
		&uploadRepoStub{},
		ingestionReaderStub{job: ingestiondomain.Job{
			ID: "job_1", TenantID: "tenant_1", KnowledgeBaseID: "kb_1", DocumentID: "doc_1", VersionID: "ver_1",
			Kind: "ingest", State: ingestiondomain.StateSucceeded, Stage: ingestiondomain.StageReady,
			Attempt: 1, MaxAttempts: 5, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
		}},
		&idsStub{},
		Config{Bucket: "knowledge", MaxBytes: 1024, Profiles: ports.ProcessingProfileIDs{Parser: "p", Chunker: "c", Embedding: "e", Index: "i"}, EmbeddingDimension: 128},
	)
	principal := searchdomain.Principal{TenantID: "tenant_1", PrincipalID: "principal_1", Scopes: map[string]struct{}{"knowledge.read": {}}}
	status, err := service.Status(t.Context(), principal, "job_1")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.State != "SUCCEEDED" || status.CurrentStage != "READY" {
		t.Fatalf("status = %+v", status)
	}
}
