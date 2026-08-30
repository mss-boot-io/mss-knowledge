package staticauth

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"

	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
	"github.com/mss-boot-io/mss-knowledge/internal/security"
	"github.com/mss-boot-io/mss-knowledge/internal/transport/httpapi"
)

var ErrInvalidConfig = errors.New("invalid static auth configuration")

// Config defines one single-user development credential.
type Config struct {
	Token       string
	TenantID    string
	PrincipalID string
	Scopes      []string
}

// Resolver validates a constant-time bearer token and resolves a fixed principal.
type Resolver struct {
	token     []byte
	principal searchdomain.Principal
}

// New creates a static resolver. It is intentionally rejected by production configuration.
func New(config Config) (*Resolver, error) {
	if config.Token == "" || strings.TrimSpace(config.TenantID) == "" || strings.TrimSpace(config.PrincipalID) == "" {
		return nil, fmt.Errorf("%w: token, tenant, and principal are required", ErrInvalidConfig)
	}
	scopes := make(map[string]struct{}, len(config.Scopes))
	for _, scope := range config.Scopes {
		scope = strings.TrimSpace(scope)
		if scope != "" {
			scopes[scope] = struct{}{}
		}
	}
	if len(scopes) == 0 {
		return nil, fmt.Errorf("%w: at least one scope is required", ErrInvalidConfig)
	}
	return &Resolver{
		token: []byte(config.Token),
		principal: searchdomain.Principal{
			TenantID:    strings.TrimSpace(config.TenantID),
			PrincipalID: strings.TrimSpace(config.PrincipalID),
			Scopes:      scopes,
		},
	}, nil
}

// ResolvePrincipal implements httpapi.PrincipalResolver.
func (r *Resolver) ResolvePrincipal(request *http.Request) (searchdomain.Principal, error) {
	if r == nil || len(r.token) == 0 {
		return searchdomain.Principal{}, httpapi.ErrUnauthenticated
	}
	header := strings.TrimSpace(request.Header.Get("Authorization"))
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return searchdomain.Principal{}, httpapi.ErrUnauthenticated
	}
	candidate := []byte(header[len(prefix):])
	if len(candidate) != len(r.token) || subtle.ConstantTimeCompare(candidate, r.token) != 1 {
		return searchdomain.Principal{}, httpapi.ErrUnauthenticated
	}
	return clonePrincipal(r.principal), nil
}

// Middleware authenticates an HTTP subtree and attaches the principal to its context.
func (r *Resolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		principal, err := r.ResolvePrincipal(request)
		if err != nil {
			writer.Header().Set("WWW-Authenticate", `Bearer realm="mss-knowledge"`)
			http.Error(writer, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(writer, request.WithContext(security.WithPrincipal(request.Context(), principal)))
	})
}

func clonePrincipal(principal searchdomain.Principal) searchdomain.Principal {
	result := principal
	result.Scopes = make(map[string]struct{}, len(principal.Scopes))
	for scope := range principal.Scopes {
		result.Scopes[scope] = struct{}{}
	}
	return result
}
