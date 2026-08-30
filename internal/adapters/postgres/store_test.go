package postgresadapter

import (
	"errors"
	"reflect"
	"testing"
	"time"

	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
)

func TestConfigDefaultsAndValidation(t *testing.T) {
	config := (Config{URL: "postgres://localhost/database"}).withDefaults()
	if err := config.validate(); err != nil {
		t.Fatalf("validate() error = %v", err)
	}
	if config.ApplicationName != defaultApplicationName {
		t.Fatalf("ApplicationName = %q", config.ApplicationName)
	}
	if config.MaxConnections != defaultMaxConnections || config.MinConnections != defaultMinConnections {
		t.Fatalf("unexpected connection defaults: %+v", config)
	}
	if config.ConnectTimeout != defaultConnectTimeout || config.HealthCheckPeriod != defaultHealthCheckPeriod {
		t.Fatalf("unexpected duration defaults: %+v", config)
	}
}

func TestConfigRejectsInvalidValues(t *testing.T) {
	tests := []Config{
		{},
		{URL: "postgres://localhost/database", MaxConnections: -1},
		{URL: "postgres://localhost/database", MaxConnections: 1, MinConnections: 2},
		{URL: "postgres://localhost/database", ConnectTimeout: -time.Second},
	}

	for index, config := range tests {
		config = config.withDefaults()
		if err := config.validate(); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("case %d validate() error = %v, want ErrInvalidConfig", index, err)
		}
	}
}

func TestValidatePrincipal(t *testing.T) {
	if err := validatePrincipal(searchdomain.Principal{
		TenantID:    "tenant_1",
		PrincipalID: "principal_1",
	}); err != nil {
		t.Fatalf("validatePrincipal() error = %v", err)
	}
	if err := validatePrincipal(searchdomain.Principal{TenantID: "tenant_1"}); !errors.Is(err, ErrInvalidPrincipal) {
		t.Fatalf("validatePrincipal() error = %v, want ErrInvalidPrincipal", err)
	}
}

func TestCompactUnique(t *testing.T) {
	got := compactUnique([]string{" kb_2 ", "", "kb_1", "kb_2", "  "})
	want := []string{"kb_2", "kb_1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("compactUnique() = %+v, want %+v", got, want)
	}
}

func TestNilStoreReadinessFails(t *testing.T) {
	var store *Store
	if err := store.Check(t.Context()); err == nil {
		t.Fatal("Check() error = nil, want non-nil")
	}
}
