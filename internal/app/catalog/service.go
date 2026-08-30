package catalog

import (
	"context"
	"errors"

	catalogdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/catalog"
	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

var ErrPermissionDenied = errors.New("catalog permission denied")

// Service exposes authorization-filtered knowledge-base metadata.
type Service struct {
	reader ports.KnowledgeBaseReader
}

// New creates a catalog service.
func New(reader ports.KnowledgeBaseReader) (*Service, error) {
	if reader == nil {
		return nil, errors.New("catalog reader is required")
	}
	return &Service{reader: reader}, nil
}

// List returns knowledge bases visible to the principal.
func (s *Service) List(ctx context.Context, principal searchdomain.Principal) ([]catalogdomain.KnowledgeBase, error) {
	if principal.TenantID == "" || principal.PrincipalID == "" ||
		(!principal.HasScope("knowledge.search") && !principal.HasScope("knowledge.read")) {
		return nil, ErrPermissionDenied
	}
	return s.reader.ListKnowledgeBases(ctx, principal)
}
