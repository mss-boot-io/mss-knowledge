//go:build integration

package postgresadapter

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mss-boot-io/mss-knowledge/internal/domain/document"
	"github.com/mss-boot-io/mss-knowledge/internal/domain/ingestion"
	"github.com/mss-boot-io/mss-knowledge/internal/domain/knowledge"
	"github.com/mss-boot-io/mss-knowledge/internal/foundation"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

func TestIngestionControlPlaneLifecycle(t *testing.T) {
	databaseURL := os.Getenv("MSS_KNOWLEDGE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("MSS_KNOWLEDGE_TEST_DATABASE_URL is not set")
	}

	store, err := Open(t.Context(), Config{
		URL:             databaseURL,
		ApplicationName: "mss-knowledge-ingestion-integration-test",
		MaxConnections:  4,
		MinConnections:  1,
		ConnectTimeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(store.Close)

	resetFoundationTables(t, store)
	t.Cleanup(func() { resetFoundationTables(t, store) })

	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	profiles := ports.ProcessingProfileIDs{
		Parser:    foundation.ParserProfileID,
		Chunker:   foundation.ChunkerProfileID,
		Embedding: foundation.EmbeddingProfileID,
		Index:     foundation.IndexProfileID,
	}
	if err := store.BootstrapLocal(t.Context(), BootstrapRequest{
		TenantID:           foundation.LocalTenantID,
		TenantSlug:         "local",
		TenantName:         "Local Tenant",
		PrincipalID:        foundation.LocalPrincipalID,
		PrincipalSubject:   "local-user",
		PrincipalName:      "Local User",
		KnowledgeBaseID:    foundation.LocalKnowledgeBaseID,
		KnowledgeBaseSlug:  "local",
		KnowledgeBaseName:  "Local Knowledge",
		DefaultLanguage:    "chinese",
		Profiles:           profiles,
		EmbeddingDimension: 128,
		CreatedAt:          createdAt,
	}); err != nil {
		t.Fatalf("BootstrapLocal() error = %v", err)
	}

	allowed, err := store.CanWriteKnowledgeBase(
		t.Context(),
		foundation.LocalTenantID,
		foundation.LocalPrincipalID,
		foundation.LocalKnowledgeBaseID,
	)
	if err != nil || !allowed {
		t.Fatalf("CanWriteKnowledgeBase() = %v, %v", allowed, err)
	}

	createRequest := ports.CreateUploadRequest{
		TenantID:        foundation.LocalTenantID,
		PrincipalID:     foundation.LocalPrincipalID,
		KnowledgeBaseID: foundation.LocalKnowledgeBaseID,
		DocumentID:      "doc_integration",
		VersionID:       "ver_integration",
		JobID:           "job_integration",
		ExternalKey:     "integration.md",
		Title:           "Integration document",
		Filename:        "integration.md",
		MediaType:       "text/markdown",
		SourceURI:       "s3://mss-knowledge/integration.md?versionId=source-v1",
		Original: ports.ObjectRef{
			Bucket:    "mss-knowledge",
			Key:       "tenants/tenant_local/integration.md",
			VersionID: "source-v1",
			ETag:      "source-etag",
			Size:      128,
			SHA256:    strings.Repeat("a", 64),
			MediaType: "text/markdown",
		},
		Profiles:            profiles,
		PipelineFingerprint: strings.Repeat("f", 64),
		CreatedAt:           createdAt,
	}
	created, err := store.CreateUpload(t.Context(), createRequest)
	if err != nil {
		t.Fatalf("CreateUpload() error = %v", err)
	}
	if created.DocumentID != createRequest.DocumentID || created.VersionNumber != 1 {
		t.Fatalf("CreateUpload() = %+v", created)
	}

	job, err := store.GetJob(t.Context(), foundation.LocalTenantID, ingestion.JobID(createRequest.JobID))
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if job.State != ingestion.StatePending || job.Stage != ingestion.StageReceived {
		t.Fatalf("initial job = %+v", job)
	}

	claimedAt := createdAt.Add(time.Second)
	jobPointer, err := store.ClaimNext(t.Context(), "worker-integration", claimedAt, 10*time.Minute)
	if err != nil {
		t.Fatalf("ClaimNext() error = %v", err)
	}
	if jobPointer == nil || jobPointer.ID != ingestion.JobID(createRequest.JobID) || jobPointer.Attempt != 1 {
		t.Fatalf("ClaimNext() = %+v", jobPointer)
	}
	job = *jobPointer

	input, err := store.LoadVersionInput(t.Context(), foundation.LocalTenantID, createRequest.VersionID)
	if err != nil {
		t.Fatalf("LoadVersionInput() error = %v", err)
	}
	if input.Original.VersionID != "source-v1" || input.Profiles != profiles {
		t.Fatalf("LoadVersionInput() = %+v", input)
	}

	chunk := knowledge.Chunk{
		ID:              "chk_integration",
		DocumentID:      createRequest.DocumentID,
		VersionID:       createRequest.VersionID,
		KnowledgeBaseID: createRequest.KnowledgeBaseID,
		Ordinal:         0,
		ContentType:     knowledge.BlockParagraph,
		HeadingPath:     []string{"Integration"},
		Text:            "Redis provides the rebuildable context projection.",
		TokenCount:      8,
		ContentSHA256:   strings.Repeat("b", 64),
	}
	if err := store.ReplaceVersionChunks(t.Context(), foundation.LocalTenantID, createRequest.VersionID, []ports.StoredChunk{{
		Chunk:         chunk,
		TextObjectKey: "tenants/tenant_local/chunks/chk_integration.txt",
	}}); err != nil {
		t.Fatalf("ReplaceVersionChunks() error = %v", err)
	}

	artifacts := ports.VersionArtifacts{
		Original: createRequest.Original,
		Normalized: ports.ObjectRef{
			Bucket:    "mss-knowledge",
			Key:       "tenants/tenant_local/normalized/document.json",
			VersionID: "normalized-v1",
			Size:      256,
			SHA256:    strings.Repeat("c", 64),
			MediaType: "application/json",
		},
		Manifest: ports.ObjectRef{
			Bucket:    "mss-knowledge",
			Key:       "tenants/tenant_local/chunks/manifest.json",
			VersionID: "manifest-v1",
			Size:      128,
			SHA256:    strings.Repeat("d", 64),
			MediaType: "application/json",
		},
		ChunkCount: 1,
		TokenCount: 8,
	}
	if err := store.SaveArtifacts(t.Context(), document.VersionID(createRequest.VersionID), artifacts); err != nil {
		t.Fatalf("SaveArtifacts() error = %v", err)
	}
	stageTime := claimedAt.Add(2 * time.Second)
	for job.Stage != ingestion.StagePublishing {
		next, ok := ingestion.NextStage(job.Stage)
		if !ok {
			t.Fatalf("no stage after %s before PUBLISHING", job.Stage)
		}
		if err := job.Advance("worker-integration", next, stageTime); err != nil {
			t.Fatalf("Advance(%s) error = %v", next, err)
		}
		leaseExpiresAt := stageTime.Add(10 * time.Minute)
		job.LeaseExpiresAt = &leaseExpiresAt
		if err := store.Save(t.Context(), job); err != nil {
			t.Fatalf("Save(%s) error = %v", next, err)
		}
		stageTime = stageTime.Add(time.Millisecond)
	}
	if err := store.PublishVersion(t.Context(), ports.PublishVersionRequest{
		TenantID:        foundation.LocalTenantID,
		KnowledgeBaseID: foundation.LocalKnowledgeBaseID,
		DocumentID:      document.DocumentID(createRequest.DocumentID),
		VersionID:       document.VersionID(createRequest.VersionID),
		JobID:           job.ID,
		JobAttempt:      job.Attempt,
		LeaseOwner:      "worker-integration",
		PublishedAt:     stageTime,
		Artifacts:       artifacts,
	}); err != nil {
		t.Fatalf("PublishVersion() error = %v", err)
	}

	activeVersions, err := store.ActiveVersionIDs(t.Context(), foundation.LocalTenantID, []string{createRequest.DocumentID})
	if err != nil {
		t.Fatalf("ActiveVersionIDs() error = %v", err)
	}
	if activeVersions[createRequest.DocumentID] != createRequest.VersionID {
		t.Fatalf("active versions = %+v", activeVersions)
	}
	completed, err := store.GetJob(t.Context(), foundation.LocalTenantID, ingestion.JobID(createRequest.JobID))
	if err != nil {
		t.Fatalf("GetJob(completed) error = %v", err)
	}
	if completed.State != ingestion.StateSucceeded || completed.Stage != ingestion.StageReady || completed.CompletedAt == nil {
		t.Fatalf("completed job = %+v", completed)
	}
	if completed.LeaseOwner != "" || completed.LeaseExpiresAt != nil {
		t.Fatalf("completed job retained lease = %+v", completed)
	}

	var outboxEvents int
	if err := store.pool.QueryRow(t.Context(), `
SELECT count(*)
FROM outbox_events
WHERE aggregate_id = $1 AND event_type = 'knowledge.document.version.published'`, createRequest.DocumentID).Scan(&outboxEvents); err != nil {
		t.Fatalf("count outbox events: %v", err)
	}
	if outboxEvents != 1 {
		t.Fatalf("outbox event count = %d", outboxEvents)
	}
}

func TestCreateUploadRevalidatesWritePermission(t *testing.T) {
	databaseURL := os.Getenv("MSS_KNOWLEDGE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("MSS_KNOWLEDGE_TEST_DATABASE_URL is not set")
	}
	store, err := Open(t.Context(), Config{URL: databaseURL, MaxConnections: 2, MinConnections: 1, ConnectTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	resetFoundationTables(t, store)
	t.Cleanup(func() { resetFoundationTables(t, store) })
	seedControlPlane(t, store)

	request := ports.CreateUploadRequest{
		TenantID: "tenant_1", PrincipalID: "principal_disabled", KnowledgeBaseID: "kb_1",
		DocumentID: "doc_denied", VersionID: "ver_denied", JobID: "job_denied",
		ExternalKey: "denied", Title: "Denied", Filename: "denied.md", MediaType: "text/markdown",
		SourceURI:           "s3://knowledge/denied.md?versionId=v1",
		Original:            ports.ObjectRef{Bucket: "knowledge", Key: "denied.md", VersionID: "v1", Size: 10, SHA256: strings.Repeat("e", 64), MediaType: "text/markdown"},
		Profiles:            ports.ProcessingProfileIDs{Parser: "profile_parser", Chunker: "profile_chunker", Embedding: "profile_embedding", Index: "profile_index"},
		PipelineFingerprint: strings.Repeat("f", 64), CreatedAt: time.Now().UTC(),
	}
	_, err = store.CreateUpload(t.Context(), request)
	if !errors.Is(err, ports.ErrPermissionDenied) {
		t.Fatalf("CreateUpload() error = %v", err)
	}
}
