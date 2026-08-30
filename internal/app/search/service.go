package searchapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

var (
	// ErrPermissionDenied is returned when the caller lacks the required scope or resource access.
	ErrPermissionDenied = errors.New("search permission denied")
	// ErrInvalidDependency is returned when the application service is constructed incorrectly.
	ErrInvalidDependency = errors.New("invalid search service dependency")
)

const (
	ScopeKnowledgeSearch      = "knowledge.search"
	ScopeKnowledgeDiagnostics = "knowledge.diagnostics"
)

// Config controls bounded retrieval and diversity behavior.
type Config struct {
	MaxTopK             int
	CandidateMultiplier int
	MaxHitsPerDocument  int
}

// Service orchestrates authorization, backend retrieval, version checks, and final result filtering.
type Service struct {
	store      ports.SearchStore
	authorizer ports.SearchAuthorizer
	versions   ports.ActiveVersionReader
	ids        ports.QueryIDGenerator
	config     Config
}

// New creates a search application service.
func New(
	store ports.SearchStore,
	authorizer ports.SearchAuthorizer,
	versions ports.ActiveVersionReader,
	ids ports.QueryIDGenerator,
	config Config,
) (*Service, error) {
	if store == nil || authorizer == nil || versions == nil || ids == nil {
		return nil, fmt.Errorf("%w: dependencies must not be nil", ErrInvalidDependency)
	}
	if config.MaxTopK <= 0 {
		config.MaxTopK = 20
	}
	if config.CandidateMultiplier <= 0 {
		config.CandidateMultiplier = 5
	}
	if config.MaxHitsPerDocument <= 0 {
		config.MaxHitsPerDocument = 3
	}
	return &Service{
		store:      store,
		authorizer: authorizer,
		versions:   versions,
		ids:        ids,
		config:     config,
	}, nil
}

// Search returns only hits that remain authorized and belong to active document versions.
func (s *Service) Search(
	ctx context.Context,
	principal searchdomain.Principal,
	request searchdomain.Request,
) (searchdomain.Response, error) {
	request = request.WithDefaults()
	if err := request.Validate(s.config.MaxTopK); err != nil {
		return searchdomain.Response{}, err
	}
	if strings.TrimSpace(principal.TenantID) == "" || strings.TrimSpace(principal.PrincipalID) == "" ||
		!principal.HasScope(ScopeKnowledgeSearch) {
		return searchdomain.Response{}, ErrPermissionDenied
	}
	if request.Include.Diagnostics && !principal.HasScope(ScopeKnowledgeDiagnostics) {
		return searchdomain.Response{}, ErrPermissionDenied
	}

	allowedKnowledgeBases, err := s.authorizer.AllowedKnowledgeBases(ctx, principal, request.KnowledgeBaseIDs)
	if err != nil {
		return searchdomain.Response{}, fmt.Errorf("resolve allowed knowledge bases: %w", err)
	}
	allowedKnowledgeBases = uniqueNonBlank(allowedKnowledgeBases)

	queryID, err := s.ids.NewQueryID()
	if err != nil {
		return searchdomain.Response{}, fmt.Errorf("create query ID: %w", err)
	}
	response := searchdomain.Response{
		QueryID: queryID,
		Mode:    request.Mode,
		Results: make([]searchdomain.Hit, 0, request.TopK),
	}
	if len(allowedKnowledgeBases) == 0 {
		return response, nil
	}

	candidateLimit := request.TopK * s.config.CandidateMultiplier
	maximumCandidates := s.config.MaxTopK * s.config.CandidateMultiplier
	if candidateLimit > maximumCandidates {
		candidateLimit = maximumCandidates
	}
	if candidateLimit < request.TopK {
		candidateLimit = request.TopK
	}

	hits, err := s.store.Search(ctx, searchdomain.StoreRequest{
		TenantID:           principal.TenantID,
		KnowledgeBaseIDs:   allowedKnowledgeBases,
		Query:              strings.TrimSpace(request.Query),
		Mode:               request.Mode,
		CandidateLimit:     candidateLimit,
		Filters:            request.Filters,
		IncludeDiagnostics: request.Include.Diagnostics,
	})
	if err != nil {
		return searchdomain.Response{}, fmt.Errorf("search store: %w", err)
	}

	allowedSet := makeStringSet(allowedKnowledgeBases)
	documentIDs := make([]string, 0, len(hits))
	seenDocuments := make(map[string]struct{}, len(hits))
	for _, hit := range hits {
		if hit.TenantID != principal.TenantID {
			continue
		}
		if _, ok := allowedSet[hit.KnowledgeBaseID]; !ok {
			continue
		}
		if _, seen := seenDocuments[hit.DocumentID]; seen || strings.TrimSpace(hit.DocumentID) == "" {
			continue
		}
		seenDocuments[hit.DocumentID] = struct{}{}
		documentIDs = append(documentIDs, hit.DocumentID)
	}

	activeVersions, err := s.versions.ActiveVersionIDs(ctx, principal.TenantID, documentIDs)
	if err != nil {
		return searchdomain.Response{}, fmt.Errorf("resolve active document versions: %w", err)
	}

	seenChunks := make(map[string]struct{}, request.TopK)
	hitsPerDocument := make(map[string]int, request.TopK)
	for _, hit := range hits {
		if len(response.Results) >= request.TopK {
			break
		}
		if !eligibleCandidate(hit, principal.TenantID, allowedSet, activeVersions) {
			continue
		}
		if _, duplicate := seenChunks[hit.ID]; duplicate || strings.TrimSpace(hit.ID) == "" {
			continue
		}
		if hitsPerDocument[hit.DocumentID] >= s.config.MaxHitsPerDocument {
			continue
		}

		allowed, err := s.authorizer.CanReadHit(ctx, principal, hit)
		if err != nil {
			return searchdomain.Response{}, fmt.Errorf("authorize search hit: %w", err)
		}
		if !allowed {
			continue
		}

		if !request.Include.Scores {
			hit.Scores = nil
		}
		seenChunks[hit.ID] = struct{}{}
		hitsPerDocument[hit.DocumentID]++
		response.Results = append(response.Results, hit)
	}

	return response, nil
}

func eligibleCandidate(
	hit searchdomain.Hit,
	tenantID string,
	allowedKnowledgeBases map[string]struct{},
	activeVersions map[string]string,
) bool {
	if hit.TenantID != tenantID {
		return false
	}
	if _, ok := allowedKnowledgeBases[hit.KnowledgeBaseID]; !ok {
		return false
	}
	activeVersion, ok := activeVersions[hit.DocumentID]
	if !ok || activeVersion == "" || activeVersion != hit.VersionID {
		return false
	}
	return true
}

func uniqueNonBlank(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func makeStringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
