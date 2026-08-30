package ports

import (
	"context"
	"errors"

	"github.com/mss-boot-io/mss-knowledge/internal/domain/catalog"
	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
)

// ErrNotFound is returned when an authorized resource cannot be found.
var ErrNotFound = errors.New("resource not found")

// KnowledgeBaseReader lists only knowledge bases visible to the caller.
type KnowledgeBaseReader interface {
	ListKnowledgeBases(ctx context.Context, principal searchdomain.Principal) ([]catalog.KnowledgeBase, error)
}

// ChunkReader retrieves a tenant-scoped search projection by stable chunk ID.
type ChunkReader interface {
	GetChunk(ctx context.Context, tenantID, chunkID string) (searchdomain.Hit, error)
}
