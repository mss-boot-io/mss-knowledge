package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	catalogdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/catalog"
	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
	"github.com/mss-boot-io/mss-knowledge/internal/security"
)

type fakeSearch struct {
	principal searchdomain.Principal
	request   searchdomain.Request
}

func (f *fakeSearch) Search(_ context.Context, principal searchdomain.Principal, request searchdomain.Request) (searchdomain.Response, error) {
	f.principal = principal
	f.request = request
	return searchdomain.Response{
		QueryID: "qry_test",
		Mode:    searchdomain.ModeBalanced,
		Results: []searchdomain.Hit{{
			ID:              "chk_1",
			KnowledgeBaseID: "kb_1",
			DocumentID:      "doc_1",
			VersionID:       "ver_1",
			Title:           "Architecture",
			Text:            "S3 is the source of truth.",
			SourceURI:       "knowledge://kb_1/doc_1/ver_1",
			ContentSHA256:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			UpdatedAt:       time.Unix(1, 0).UTC(),
		}},
	}, nil
}

type fakeFetch struct{}

func (fakeFetch) Fetch(_ context.Context, _ searchdomain.Principal, id string) (searchdomain.Hit, error) {
	return searchdomain.Hit{ID: id, Text: "full chunk"}, nil
}

type fakeCatalog struct{}

func (fakeCatalog) List(context.Context, searchdomain.Principal) ([]catalogdomain.KnowledgeBase, error) {
	return []catalogdomain.KnowledgeBase{{ID: "kb_1", Slug: "default", Name: "Default", Revision: 1}}, nil
}

func TestStreamableHTTPExposesReadOnlyTools(t *testing.T) {
	search := &fakeSearch{}
	handler, err := NewHandler(Options{
		Name:    "mss-knowledge-test",
		Version: "test",
		Search:  search,
		Fetch:   fakeFetch{},
		Catalog: fakeCatalog{},
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	principal := searchdomain.Principal{
		TenantID:    "tenant_1",
		PrincipalID: "principal_1",
		Scopes: map[string]struct{}{
			"knowledge.search": {},
			"knowledge.read":   {},
		},
	}
	authorized := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handler.ServeHTTP(writer, request.WithContext(security.WithPrincipal(request.Context(), principal)))
	})
	server := httptest.NewServer(authorized)
	defer server.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL}, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools.Tools) != 3 {
		t.Fatalf("tool count = %d, want 3", len(tools.Tools))
	}
	for _, tool := range tools.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("tool %q is not marked read-only", tool.Name)
		}
	}

	searchResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query":              "source of truth",
			"knowledge_base_ids": []string{"kb_1"},
			"mode":               "balanced",
			"top_k":              4,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search) error = %v", err)
	}
	if searchResult.IsError || len(searchResult.Content) != 1 {
		t.Fatalf("search result = %+v", searchResult)
	}
	if search.principal.TenantID != "tenant_1" || search.request.Query != "source of truth" || search.request.TopK != 4 {
		t.Fatalf("search invocation = principal %+v request %+v", search.principal, search.request)
	}
	text, ok := searchResult.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("search content type = %T", searchResult.Content[0])
	}
	var response searchdomain.Response
	if err := json.Unmarshal([]byte(text.Text), &response); err != nil {
		t.Fatalf("decode search output: %v", err)
	}
	if response.QueryID != "qry_test" || len(response.Results) != 1 {
		t.Fatalf("search output = %+v", response)
	}

	fetchResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "fetch",
		Arguments: map[string]any{"id": "chk_1"},
	})
	if err != nil || fetchResult.IsError {
		t.Fatalf("CallTool(fetch) result = %+v error = %v", fetchResult, err)
	}

	catalogResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_knowledge_bases",
		Arguments: map[string]any{},
	})
	if err != nil || catalogResult.IsError {
		t.Fatalf("CallTool(list_knowledge_bases) result = %+v error = %v", catalogResult, err)
	}
}

func TestHandlerRejectsMissingDependencies(t *testing.T) {
	if _, err := NewHandler(Options{Name: "test", Version: "test"}); err == nil {
		t.Fatal("NewHandler() error = nil, want invalid options")
	}
}
