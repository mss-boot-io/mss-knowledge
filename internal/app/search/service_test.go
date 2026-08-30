package searchapp

import (
	"context"
	"errors"
	"testing"

	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
)

type fakeSearchStore struct {
	hits    []searchdomain.Hit
	request searchdomain.StoreRequest
	err     error
}

func (f *fakeSearchStore) Search(_ context.Context, request searchdomain.StoreRequest) ([]searchdomain.Hit, error) {
	f.request = request
	return f.hits, f.err
}

func (f *fakeSearchStore) Check(context.Context) error { return nil }

type fakeAuthorizer struct {
	allowedKBs []string
	deniedHits map[string]bool
}

func (f *fakeAuthorizer) AllowedKnowledgeBases(
	_ context.Context,
	_ searchdomain.Principal,
	_ []string,
) ([]string, error) {
	return f.allowedKBs, nil
}

func (f *fakeAuthorizer) CanReadHit(
	_ context.Context,
	_ searchdomain.Principal,
	hit searchdomain.Hit,
) (bool, error) {
	return !f.deniedHits[hit.ID], nil
}

type fakeVersions struct {
	active map[string]string
}

func (f *fakeVersions) ActiveVersionIDs(
	_ context.Context,
	_ string,
	_ []string,
) (map[string]string, error) {
	return f.active, nil
}

type fixedIDs struct{}

func (fixedIDs) NewQueryID() (string, error) { return "qry_test", nil }

func TestServiceFiltersUnauthorizedStaleAndDuplicateHits(t *testing.T) {
	store := &fakeSearchStore{hits: []searchdomain.Hit{
		{ID: "chunk_1", TenantID: "tenant_1", KnowledgeBaseID: "kb_1", DocumentID: "doc_1", VersionID: "ver_active", Scores: &searchdomain.Scores{}},
		{ID: "chunk_1", TenantID: "tenant_1", KnowledgeBaseID: "kb_1", DocumentID: "doc_1", VersionID: "ver_active"},
		{ID: "chunk_2", TenantID: "tenant_1", KnowledgeBaseID: "kb_1", DocumentID: "doc_1", VersionID: "ver_active"},
		{ID: "chunk_3", TenantID: "tenant_1", KnowledgeBaseID: "kb_1", DocumentID: "doc_1", VersionID: "ver_active"},
		{ID: "chunk_4", TenantID: "tenant_1", KnowledgeBaseID: "kb_1", DocumentID: "doc_1", VersionID: "ver_active"},
		{ID: "chunk_stale", TenantID: "tenant_1", KnowledgeBaseID: "kb_1", DocumentID: "doc_2", VersionID: "ver_old"},
		{ID: "chunk_wrong_kb", TenantID: "tenant_1", KnowledgeBaseID: "kb_2", DocumentID: "doc_3", VersionID: "ver_3"},
		{ID: "chunk_wrong_tenant", TenantID: "tenant_2", KnowledgeBaseID: "kb_1", DocumentID: "doc_4", VersionID: "ver_4"},
		{ID: "chunk_denied", TenantID: "tenant_1", KnowledgeBaseID: "kb_1", DocumentID: "doc_5", VersionID: "ver_5"},
	}}
	authorizer := &fakeAuthorizer{
		allowedKBs: []string{"kb_1", "kb_1", ""},
		deniedHits: map[string]bool{"chunk_denied": true},
	}
	versions := &fakeVersions{active: map[string]string{
		"doc_1": "ver_active",
		"doc_2": "ver_new",
		"doc_5": "ver_5",
	}}

	service, err := New(store, authorizer, versions, fixedIDs{}, Config{
		MaxTopK:             10,
		CandidateMultiplier: 5,
		MaxHitsPerDocument:  3,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	response, err := service.Search(context.Background(), searchdomain.Principal{
		TenantID:    "tenant_1",
		PrincipalID: "principal_1",
		Scopes:      map[string]struct{}{ScopeKnowledgeSearch: {}},
	}, searchdomain.Request{Query: "architecture", Mode: searchdomain.ModeBalanced, TopK: 8})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if response.QueryID != "qry_test" || response.Mode != searchdomain.ModeBalanced {
		t.Fatalf("unexpected response metadata: %+v", response)
	}
	if len(response.Results) != 3 {
		t.Fatalf("len(Results) = %d, want 3: %+v", len(response.Results), response.Results)
	}
	for _, hit := range response.Results {
		if hit.DocumentID != "doc_1" {
			t.Fatalf("unexpected hit survived: %+v", hit)
		}
		if hit.Scores != nil {
			t.Fatalf("scores were not requested: %+v", hit.Scores)
		}
	}
	if store.request.CandidateLimit != 40 {
		t.Fatalf("CandidateLimit = %d, want 40", store.request.CandidateLimit)
	}
	if len(store.request.KnowledgeBaseIDs) != 1 || store.request.KnowledgeBaseIDs[0] != "kb_1" {
		t.Fatalf("unexpected authorized KBs: %+v", store.request.KnowledgeBaseIDs)
	}
}

func TestServiceRequiresSearchScope(t *testing.T) {
	service, err := New(
		&fakeSearchStore{},
		&fakeAuthorizer{},
		&fakeVersions{},
		fixedIDs{},
		Config{},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = service.Search(context.Background(), searchdomain.Principal{
		TenantID:    "tenant_1",
		PrincipalID: "principal_1",
		Scopes:      map[string]struct{}{},
	}, searchdomain.Request{Query: "query"})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("Search() error = %v, want ErrPermissionDenied", err)
	}
}

func TestServiceReturnsEmptyWithoutAuthorizedKnowledgeBases(t *testing.T) {
	store := &fakeSearchStore{}
	service, err := New(
		store,
		&fakeAuthorizer{allowedKBs: nil},
		&fakeVersions{},
		fixedIDs{},
		Config{},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	response, err := service.Search(context.Background(), searchdomain.Principal{
		TenantID:    "tenant_1",
		PrincipalID: "principal_1",
		Scopes:      map[string]struct{}{ScopeKnowledgeSearch: {}},
	}, searchdomain.Request{Query: "query"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(response.Results) != 0 {
		t.Fatalf("Results = %+v, want empty", response.Results)
	}
	if store.request.Query != "" {
		t.Fatalf("search store was unexpectedly called: %+v", store.request)
	}
}
