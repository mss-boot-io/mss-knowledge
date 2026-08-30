package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	catalogdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/catalog"
	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
	"github.com/mss-boot-io/mss-knowledge/internal/security"
)

var ErrInvalidOptions = errors.New("invalid MCP server options")

// SearchUseCase is implemented by the knowledge search application service.
type SearchUseCase interface {
	Search(context.Context, searchdomain.Principal, searchdomain.Request) (searchdomain.Response, error)
}

// FetchUseCase retrieves one active and authorized chunk.
type FetchUseCase interface {
	Fetch(context.Context, searchdomain.Principal, string) (searchdomain.Hit, error)
}

// CatalogUseCase lists knowledge bases visible to a principal.
type CatalogUseCase interface {
	List(context.Context, searchdomain.Principal) ([]catalogdomain.KnowledgeBase, error)
}

// Options controls the read-only MCP server.
type Options struct {
	Name    string
	Version string
	Search  SearchUseCase
	Fetch   FetchUseCase
	Catalog CatalogUseCase
}

// SearchInput is the stable MCP search contract.
type SearchInput struct {
	Query            string   `json:"query" jsonschema:"The question or search phrase to retrieve supporting knowledge for."`
	KnowledgeBaseIDs []string `json:"knowledge_base_ids,omitempty" jsonschema:"Optional knowledge-base IDs. Omit to search every knowledge base authorized for the caller."`
	Mode             string   `json:"mode,omitempty" jsonschema:"Retrieval mode: exact, fast, or balanced. Defaults to balanced."`
	TopK             int      `json:"top_k,omitempty" jsonschema:"Maximum number of active authorized chunks to return. Defaults to 8."`
	IncludeScores    bool     `json:"include_scores,omitempty" jsonschema:"Include backend retrieval scores for diagnostics."`
}

// FetchInput is the stable MCP fetch contract.
type FetchInput struct {
	ID string `json:"id" jsonschema:"The stable chunk ID returned by the search tool."`
}

// ListInput intentionally has no fields.
type ListInput struct{}

// NewHandler returns a Streamable HTTP MCP handler with read-only search/fetch/catalog tools.
func NewHandler(options Options) (http.Handler, error) {
	options.Name = strings.TrimSpace(options.Name)
	options.Version = strings.TrimSpace(options.Version)
	if options.Name == "" || options.Version == "" || options.Search == nil || options.Fetch == nil || options.Catalog == nil {
		return nil, fmt.Errorf("%w: name, version, search, fetch, and catalog are required", ErrInvalidOptions)
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    options.Name,
		Version: options.Version,
	}, &mcp.ServerOptions{
		Instructions: "Use search to find relevant knowledge, then fetch exact chunks when more context is required. All tools are read-only and authorization-filtered.",
	})

	closedWorld := false
	annotations := &mcp.ToolAnnotations{
		ReadOnlyHint:    true,
		IdempotentHint:  true,
		DestructiveHint: boolPointer(false),
		OpenWorldHint:   &closedWorld,
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search",
		Title:       "Search MSS Knowledge",
		Description: "Search active, authorized knowledge chunks using exact or hybrid retrieval and return versioned citations.",
		Annotations: cloneAnnotations(annotations),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input *SearchInput) (*mcp.CallToolResult, any, error) {
		principal, err := authenticatedPrincipal(ctx)
		if err != nil {
			return nil, nil, err
		}
		if input == nil {
			return nil, nil, fmt.Errorf("search input is required")
		}
		mode := searchdomain.Mode(strings.ToLower(strings.TrimSpace(input.Mode)))
		response, err := options.Search.Search(ctx, principal, searchdomain.Request{
			Query:            input.Query,
			KnowledgeBaseIDs: append([]string(nil), input.KnowledgeBaseIDs...),
			Mode:             mode,
			TopK:             input.TopK,
			Include: searchdomain.Include{
				Scores: input.IncludeScores,
			},
		})
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(response)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "fetch",
		Title:       "Fetch MSS Knowledge Chunk",
		Description: "Fetch one exact active and authorized chunk by the stable ID returned from search.",
		Annotations: cloneAnnotations(annotations),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input *FetchInput) (*mcp.CallToolResult, any, error) {
		principal, err := authenticatedPrincipal(ctx)
		if err != nil {
			return nil, nil, err
		}
		if input == nil {
			return nil, nil, fmt.Errorf("fetch input is required")
		}
		hit, err := options.Fetch.Fetch(ctx, principal, input.ID)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(hit)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_knowledge_bases",
		Title:       "List MSS Knowledge Bases",
		Description: "List the knowledge bases currently visible to the authenticated caller.",
		Annotations: cloneAnnotations(annotations),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ *ListInput) (*mcp.CallToolResult, any, error) {
		principal, err := authenticatedPrincipal(ctx)
		if err != nil {
			return nil, nil, err
		}
		items, err := options.Catalog.List(ctx, principal)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]any{"knowledge_bases": items})
	})

	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil), nil
}

func authenticatedPrincipal(ctx context.Context) (searchdomain.Principal, error) {
	principal, ok := security.PrincipalFromContext(ctx)
	if !ok || strings.TrimSpace(principal.TenantID) == "" || strings.TrimSpace(principal.PrincipalID) == "" {
		return searchdomain.Principal{}, fmt.Errorf("authentication is required")
	}
	return principal, nil
}

func jsonResult(value any) (*mcp.CallToolResult, any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, nil, fmt.Errorf("encode MCP result: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
	}, nil, nil
}

func boolPointer(value bool) *bool { return &value }

func cloneAnnotations(input *mcp.ToolAnnotations) *mcp.ToolAnnotations {
	if input == nil {
		return nil
	}
	copy := *input
	if input.DestructiveHint != nil {
		value := *input.DestructiveHint
		copy.DestructiveHint = &value
	}
	if input.OpenWorldHint != nil {
		value := *input.OpenWorldHint
		copy.OpenWorldHint = &value
	}
	return &copy
}
