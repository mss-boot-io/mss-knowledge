package ports

import (
	"context"

	"github.com/mss-boot-io/mss-knowledge/internal/domain/search"
)

// SearchStore executes an already-authorized backend-neutral retrieval request.
type SearchStore interface {
	Search(ctx context.Context, request search.StoreRequest) ([]search.Hit, error)
	Check(ctx context.Context) error
}

// SearchAuthorizer resolves candidate knowledge bases and validates final hits.
type SearchAuthorizer interface {
	AllowedKnowledgeBases(
		ctx context.Context,
		principal search.Principal,
		requested []string,
	) ([]string, error)

	CanReadHit(
		ctx context.Context,
		principal search.Principal,
		hit search.Hit,
	) (bool, error)
}

// ActiveVersionReader returns the authoritative active version for each document.
type ActiveVersionReader interface {
	ActiveVersionIDs(
		ctx context.Context,
		tenantID string,
		documentIDs []string,
	) (map[string]string, error)
}

// QueryIDGenerator creates opaque query identifiers for tracing and pagination.
type QueryIDGenerator interface {
	NewQueryID() (string, error)
}
