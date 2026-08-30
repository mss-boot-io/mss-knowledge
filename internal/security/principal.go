package security

import (
	"context"

	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
)

type principalKey struct{}

// WithPrincipal attaches an already authenticated principal to a request context.
func WithPrincipal(ctx context.Context, principal searchdomain.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

// PrincipalFromContext returns the authenticated principal attached by a transport middleware.
func PrincipalFromContext(ctx context.Context) (searchdomain.Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(searchdomain.Principal)
	return principal, ok
}
