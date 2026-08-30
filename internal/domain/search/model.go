package search

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Mode selects the retrieval strategy exposed through REST and MCP.
type Mode string

const (
	ModeExact    Mode = "exact"
	ModeFast     Mode = "fast"
	ModeBalanced Mode = "balanced"
)

var ErrInvalidRequest = errors.New("invalid search request")

// Request is the backend-neutral public search request.
type Request struct {
	Query            string   `json:"query"`
	KnowledgeBaseIDs []string `json:"knowledge_base_ids,omitempty"`
	Mode             Mode     `json:"mode,omitempty"`
	TopK             int      `json:"top_k,omitempty"`
	Filters          Filters  `json:"filters,omitempty"`
	Include          Include  `json:"include,omitempty"`
}

// Filters narrows search within the caller's authorized knowledge bases.
type Filters struct {
	DocumentIDs   []string   `json:"document_ids,omitempty"`
	ContentTypes  []string   `json:"content_types,omitempty"`
	Languages     []string   `json:"languages,omitempty"`
	UpdatedAfter  *time.Time `json:"updated_after,omitempty"`
	UpdatedBefore *time.Time `json:"updated_before,omitempty"`
}

// Include controls optional diagnostic response fields.
type Include struct {
	Scores      bool `json:"scores,omitempty"`
	Diagnostics bool `json:"diagnostics,omitempty"`
}

// WithDefaults applies stable server-independent request defaults.
func (r Request) WithDefaults() Request {
	if r.Mode == "" {
		r.Mode = ModeBalanced
	}
	if r.TopK == 0 {
		r.TopK = 8
	}
	return r
}

// Validate checks public request invariants. The caller supplies its configured top-K limit.
func (r Request) Validate(maxTopK int) error {
	if strings.TrimSpace(r.Query) == "" {
		return fmt.Errorf("%w: query must not be empty", ErrInvalidRequest)
	}
	if len([]rune(r.Query)) > 4096 {
		return fmt.Errorf("%w: query is too long", ErrInvalidRequest)
	}
	switch r.Mode {
	case ModeExact, ModeFast, ModeBalanced:
	default:
		return fmt.Errorf("%w: unsupported mode %q", ErrInvalidRequest, r.Mode)
	}
	if maxTopK <= 0 {
		return fmt.Errorf("%w: server maximum must be positive", ErrInvalidRequest)
	}
	if r.TopK <= 0 || r.TopK > maxTopK {
		return fmt.Errorf("%w: top_k must be between 1 and %d", ErrInvalidRequest, maxTopK)
	}
	if r.Filters.UpdatedAfter != nil && r.Filters.UpdatedBefore != nil &&
		r.Filters.UpdatedAfter.After(*r.Filters.UpdatedBefore) {
		return fmt.Errorf("%w: updated_after must not be after updated_before", ErrInvalidRequest)
	}
	if hasBlank(r.KnowledgeBaseIDs) || hasBlank(r.Filters.DocumentIDs) ||
		hasBlank(r.Filters.ContentTypes) || hasBlank(r.Filters.Languages) {
		return fmt.Errorf("%w: filter identifiers must not be blank", ErrInvalidRequest)
	}
	return nil
}

// Principal carries the authenticated identity required by application services.
type Principal struct {
	TenantID    string
	PrincipalID string
	Scopes      map[string]struct{}
}

// HasScope reports whether the principal has the required capability.
func (p Principal) HasScope(scope string) bool {
	_, ok := p.Scopes[scope]
	return ok
}

// StoreRequest is the authorized, backend-neutral request passed to SearchStore.
type StoreRequest struct {
	TenantID           string
	KnowledgeBaseIDs   []string
	Query              string
	Mode               Mode
	CandidateLimit     int
	Filters            Filters
	IncludeDiagnostics bool
}

// Scores exposes retrieval stages without defining backend-specific score semantics.
type Scores struct {
	Lexical *float64 `json:"lexical,omitempty"`
	Vector  *float64 `json:"vector,omitempty"`
	Fused   *float64 `json:"fused,omitempty"`
	Rerank  *float64 `json:"rerank,omitempty"`
}

// Hit is a versioned piece of evidence returned by a search backend.
type Hit struct {
	ID              string    `json:"id"`
	TenantID        string    `json:"-"`
	KnowledgeBaseID string    `json:"knowledge_base_id"`
	DocumentID      string    `json:"document_id"`
	VersionID       string    `json:"version_id"`
	ParentChunkID   string    `json:"parent_chunk_id,omitempty"`
	Ordinal         int       `json:"ordinal,omitempty"`
	Title           string    `json:"title"`
	HeadingPath     []string  `json:"heading_path,omitempty"`
	Text            string    `json:"text"`
	SourceURI       string    `json:"source_uri"`
	PageStart       *int      `json:"page_start,omitempty"`
	PageEnd         *int      `json:"page_end,omitempty"`
	ContentSHA256   string    `json:"content_sha256"`
	UpdatedAt       time.Time `json:"updated_at"`
	Scores          *Scores   `json:"scores,omitempty"`
}

// Response is the public retrieval result.
type Response struct {
	QueryID    string `json:"query_id"`
	Mode       Mode   `json:"mode"`
	Degraded   bool   `json:"degraded"`
	Results    []Hit  `json:"results"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func hasBlank(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}
