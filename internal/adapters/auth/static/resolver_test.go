package staticauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolverAuthenticatesBearerToken(t *testing.T) {
	resolver, err := New(Config{
		Token:       "secret-token",
		TenantID:    "tenant_1",
		PrincipalID: "principal_1",
		Scopes:      []string{"knowledge.search"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	principal, err := resolver.ResolvePrincipal(request)
	if err != nil {
		t.Fatalf("ResolvePrincipal() error = %v", err)
	}
	if principal.TenantID != "tenant_1" || !principal.HasScope("knowledge.search") {
		t.Fatalf("principal = %+v", principal)
	}
}

func TestResolverRejectsWrongToken(t *testing.T) {
	resolver, _ := New(Config{Token: "secret-token", TenantID: "tenant_1", PrincipalID: "principal_1", Scopes: []string{"knowledge.read"}})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer wrong")
	if _, err := resolver.ResolvePrincipal(request); err == nil {
		t.Fatal("ResolvePrincipal() error = nil")
	}
}
