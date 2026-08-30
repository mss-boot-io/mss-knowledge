package fetch

import (
	"context"
	"errors"
	"testing"

	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

type chunkReaderStub struct {
	hit searchdomain.Hit
	err error
}

func (s chunkReaderStub) GetChunk(context.Context, string, string) (searchdomain.Hit, error) {
	return s.hit, s.err
}

type authorizerStub struct{ allowed bool }

func (s authorizerStub) AllowedKnowledgeBases(context.Context, searchdomain.Principal, []string) ([]string, error) {
	return nil, nil
}
func (s authorizerStub) CanReadHit(context.Context, searchdomain.Principal, searchdomain.Hit) (bool, error) {
	return s.allowed, nil
}

type versionsStub map[string]string

func (s versionsStub) ActiveVersionIDs(context.Context, string, []string) (map[string]string, error) {
	return s, nil
}

func TestFetchReturnsActiveAuthorizedChunk(t *testing.T) {
	hit := searchdomain.Hit{ID: "chunk_1", TenantID: "tenant_1", KnowledgeBaseID: "kb_1", DocumentID: "doc_1", VersionID: "ver_1"}
	service, err := New(chunkReaderStub{hit: hit}, authorizerStub{allowed: true}, versionsStub{"doc_1": "ver_1"})
	if err != nil {
		t.Fatal(err)
	}
	principal := searchdomain.Principal{TenantID: "tenant_1", PrincipalID: "principal_1", Scopes: map[string]struct{}{ScopeKnowledgeRead: {}}}
	result, err := service.Fetch(t.Context(), principal, "chunk_1")
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if result.ID != "chunk_1" {
		t.Fatalf("result = %+v", result)
	}
}

func TestFetchHidesStaleChunk(t *testing.T) {
	hit := searchdomain.Hit{ID: "chunk_1", TenantID: "tenant_1", KnowledgeBaseID: "kb_1", DocumentID: "doc_1", VersionID: "ver_old"}
	service, _ := New(chunkReaderStub{hit: hit}, authorizerStub{allowed: true}, versionsStub{"doc_1": "ver_new"})
	principal := searchdomain.Principal{TenantID: "tenant_1", PrincipalID: "principal_1", Scopes: map[string]struct{}{ScopeKnowledgeRead: {}}}
	_, err := service.Fetch(t.Context(), principal, "chunk_1")
	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Fetch() error = %v", err)
	}
}
