package fetch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

var (
	// ErrPermissionDenied is returned when the caller lacks knowledge.read.
	ErrPermissionDenied = errors.New("fetch permission denied")
	// ErrInvalidDependency is returned when a fetch service is incomplete.
	ErrInvalidDependency = errors.New("invalid fetch service dependency")
)

const ScopeKnowledgeRead = "knowledge.read"

// Service performs tenant, ACL, and active-version validation for exact chunk fetches.
type Service struct {
	chunks     ports.ChunkReader
	authorizer ports.SearchAuthorizer
	versions   ports.ActiveVersionReader
}

// New creates a fetch service.
func New(chunks ports.ChunkReader, authorizer ports.SearchAuthorizer, versions ports.ActiveVersionReader) (*Service, error) {
	if chunks == nil || authorizer == nil || versions == nil {
		return nil, ErrInvalidDependency
	}
	return &Service{chunks: chunks, authorizer: authorizer, versions: versions}, nil
}

// Fetch returns one active, authorized chunk.
func (s *Service) Fetch(ctx context.Context, principal searchdomain.Principal, chunkID string) (searchdomain.Hit, error) {
	chunkID = strings.TrimSpace(chunkID)
	if chunkID == "" {
		return searchdomain.Hit{}, fmt.Errorf("chunk ID must not be empty")
	}
	if principal.TenantID == "" || principal.PrincipalID == "" || !principal.HasScope(ScopeKnowledgeRead) {
		return searchdomain.Hit{}, ErrPermissionDenied
	}
	hit, err := s.chunks.GetChunk(ctx, principal.TenantID, chunkID)
	if err != nil {
		return searchdomain.Hit{}, err
	}
	if hit.TenantID != principal.TenantID {
		return searchdomain.Hit{}, ports.ErrNotFound
	}
	allowed, err := s.authorizer.CanReadHit(ctx, principal, hit)
	if err != nil {
		return searchdomain.Hit{}, fmt.Errorf("authorize chunk: %w", err)
	}
	if !allowed {
		return searchdomain.Hit{}, ports.ErrNotFound
	}
	activeVersions, err := s.versions.ActiveVersionIDs(ctx, principal.TenantID, []string{hit.DocumentID})
	if err != nil {
		return searchdomain.Hit{}, fmt.Errorf("resolve active version: %w", err)
	}
	if activeVersions[hit.DocumentID] != hit.VersionID {
		return searchdomain.Hit{}, ports.ErrNotFound
	}
	return hit, nil
}
