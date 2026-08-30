//go:build integration

package postgresadapter

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
)

func TestStoreAuthorizationAndActiveVersions(t *testing.T) {
	databaseURL := os.Getenv("MSS_KNOWLEDGE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("MSS_KNOWLEDGE_TEST_DATABASE_URL is not set")
	}

	store, err := Open(t.Context(), Config{
		URL:             databaseURL,
		ApplicationName: "mss-knowledge-postgres-integration-test",
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
	seedControlPlane(t, store)

	principal := searchdomain.Principal{
		TenantID:    "tenant_1",
		PrincipalID: "principal_1",
	}

	allowed, err := store.AllowedKnowledgeBases(t.Context(), principal, nil)
	if err != nil {
		t.Fatalf("AllowedKnowledgeBases(all) error = %v", err)
	}
	if want := []string{"kb_1"}; !reflect.DeepEqual(allowed, want) {
		t.Fatalf("AllowedKnowledgeBases(all) = %+v, want %+v", allowed, want)
	}

	allowed, err = store.AllowedKnowledgeBases(t.Context(), principal, []string{"kb_4", "kb_2", "kb_1", "kb_1"})
	if err != nil {
		t.Fatalf("AllowedKnowledgeBases(requested) error = %v", err)
	}
	if want := []string{"kb_1"}; !reflect.DeepEqual(allowed, want) {
		t.Fatalf("AllowedKnowledgeBases(requested) = %+v, want %+v", allowed, want)
	}

	readable, err := store.CanReadHit(t.Context(), principal, searchdomain.Hit{
		TenantID:        "tenant_1",
		KnowledgeBaseID: "kb_1",
	})
	if err != nil {
		t.Fatalf("CanReadHit(allowed) error = %v", err)
	}
	if !readable {
		t.Fatal("CanReadHit(allowed) = false")
	}

	for name, hit := range map[string]searchdomain.Hit{
		"revoked binding": {TenantID: "tenant_1", KnowledgeBaseID: "kb_2"},
		"disabled KB":     {TenantID: "tenant_1", KnowledgeBaseID: "kb_3"},
		"other tenant":    {TenantID: "tenant_2", KnowledgeBaseID: "kb_1"},
	} {
		t.Run(name, func(t *testing.T) {
			readable, err := store.CanReadHit(t.Context(), principal, hit)
			if err != nil {
				t.Fatalf("CanReadHit() error = %v", err)
			}
			if readable {
				t.Fatal("CanReadHit() = true, want false")
			}
		})
	}

	activeVersions, err := store.ActiveVersionIDs(
		t.Context(),
		"tenant_1",
		[]string{"doc_4", "doc_3", "doc_2", "doc_1", "doc_1", ""},
	)
	if err != nil {
		t.Fatalf("ActiveVersionIDs() error = %v", err)
	}
	if want := map[string]string{"doc_1": "ver_1"}; !reflect.DeepEqual(activeVersions, want) {
		t.Fatalf("ActiveVersionIDs() = %+v, want %+v", activeVersions, want)
	}

	empty, err := store.ActiveVersionIDs(t.Context(), "tenant_1", nil)
	if err != nil {
		t.Fatalf("ActiveVersionIDs(empty) error = %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("ActiveVersionIDs(empty) = %+v", empty)
	}

	if err := store.Check(t.Context()); err != nil {
		t.Fatalf("Check() error = %v", err)
	}
}

func seedControlPlane(t *testing.T, store *Store) {
	t.Helper()
	ctx := t.Context()
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	exec := func(query string, arguments ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, query, arguments...); err != nil {
			t.Fatalf("seed query failed: %v\nquery: %s", err, query)
		}
	}

	exec(`
INSERT INTO tenants (id, slug, name)
VALUES
    ('tenant_1', 'tenant-1', 'Tenant One'),
    ('tenant_2', 'tenant-2', 'Tenant Two')`)

	exec(`
INSERT INTO principals (id, tenant_id, kind, external_subject, display_name, status)
VALUES
    ('principal_1', 'tenant_1', 'user', 'subject-1', 'Principal One', 'active'),
    ('principal_disabled', 'tenant_1', 'user', 'subject-disabled', 'Disabled Principal', 'disabled'),
    ('principal_2', 'tenant_2', 'user', 'subject-2', 'Principal Two', 'active')`)

	exec(`
INSERT INTO knowledge_bases (id, tenant_id, slug, name, status, created_by)
VALUES
    ('kb_1', 'tenant_1', 'kb-1', 'Allowed KB', 'active', 'principal_1'),
    ('kb_2', 'tenant_1', 'kb-2', 'Revoked KB', 'active', 'principal_1'),
    ('kb_3', 'tenant_1', 'kb-3', 'Disabled KB', 'disabled', 'principal_1'),
    ('kb_4', 'tenant_2', 'kb-4', 'Other Tenant KB', 'active', 'principal_2')`)

	exec(`
INSERT INTO kb_acl_bindings (id, tenant_id, kb_id, principal_id, role, created_by, revoked_at)
VALUES
    ('acl_1', 'tenant_1', 'kb_1', 'principal_1', 'reader', 'principal_1', NULL),
    ('acl_2', 'tenant_1', 'kb_2', 'principal_1', 'reader', 'principal_1', now()),
    ('acl_3', 'tenant_1', 'kb_3', 'principal_1', 'reader', 'principal_1', NULL),
    ('acl_4', 'tenant_2', 'kb_4', 'principal_2', 'reader', 'principal_2', NULL),
    ('acl_5', 'tenant_1', 'kb_1', 'principal_disabled', 'reader', 'principal_1', NULL)`)

	exec(`
INSERT INTO processing_profiles (id, kind, name, version, provider, fingerprint)
VALUES
    ('profile_parser', 'parser', 'parser', '1', 'native', 'parser-fingerprint'),
    ('profile_chunker', 'chunker', 'chunker', '1', 'structural', 'chunker-fingerprint'),
    ('profile_embedding', 'embedding', 'embedding', '1', 'test', 'embedding-fingerprint'),
    ('profile_index', 'index', 'index', '1', 'redis', 'index-fingerprint')`)

	exec(`
INSERT INTO documents (id, tenant_id, kb_id, external_key, title, status, created_by)
VALUES
    ('doc_1', 'tenant_1', 'kb_1', 'doc-1', 'Ready document', 'active', 'principal_1'),
    ('doc_2', 'tenant_1', 'kb_1', 'doc-2', 'Processing document', 'active', 'principal_1'),
    ('doc_3', 'tenant_1', 'kb_3', 'Disabled KB document', 'active', 'principal_1'),
    ('doc_4', 'tenant_2', 'kb_4', 'Other tenant document', 'active', 'principal_2')`)

	insertVersion := func(id, tenantID, knowledgeBaseID, documentID, status, hashCharacter, creator string) {
		t.Helper()
		exec(`
INSERT INTO document_versions (
    id, tenant_id, kb_id, document_id, version_number, status,
    source_uri, object_bucket, object_key, filename, media_type,
    size_bytes, content_sha256,
    parser_profile_id, chunker_profile_id, embedding_profile_id, index_profile_id,
    pipeline_fingerprint, created_by, published_at
)
VALUES (
    $1, $2, $3, $4, 1, $5,
    $6, 'knowledge', $7, $8, 'text/markdown',
    100, $9,
    'profile_parser', 'profile_chunker', 'profile_embedding', 'profile_index',
    $10, $11,
    CASE WHEN $5 = 'READY' THEN now() ELSE NULL END
)`,
			id,
			tenantID,
			knowledgeBaseID,
			documentID,
			status,
			"s3://knowledge/"+documentID,
			"documents/"+documentID+"/source.md",
			documentID+".md",
			strings.Repeat(hashCharacter, 64),
			"pipeline-"+id,
			creator,
		)
	}

	insertVersion("ver_1", "tenant_1", "kb_1", "doc_1", "READY", "a", "principal_1")
	insertVersion("ver_2", "tenant_1", "kb_1", "doc_2", "PROCESSING", "b", "principal_1")
	insertVersion("ver_3", "tenant_1", "kb_3", "doc_3", "READY", "c", "principal_1")
	insertVersion("ver_4", "tenant_2", "kb_4", "doc_4", "READY", "d", "principal_2")

	exec(`
UPDATE documents
SET active_version_id = CASE id
    WHEN 'doc_1' THEN 'ver_1'
    WHEN 'doc_2' THEN 'ver_2'
    WHEN 'doc_3' THEN 'ver_3'
    WHEN 'doc_4' THEN 'ver_4'
END
WHERE id IN ('doc_1', 'doc_2', 'doc_3', 'doc_4')`)

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed transaction: %v", err)
	}
}

func resetFoundationTables(t *testing.T, store *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := store.pool.Exec(ctx, `
TRUNCATE TABLE
    outbox_events,
    audit_events,
    task_checkpoints,
    sessions,
    memories,
    ingestion_stage_runs,
    ingestion_jobs,
    chunks,
    document_versions,
    documents,
    processing_profiles,
    sources,
    kb_acl_bindings,
    knowledge_bases,
    principals,
    tenants
RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("reset foundation tables: %v", err)
	}
}
