package config

import (
	"strings"
	"testing"
	"time"
)

func TestFromLookupUsesDefaults(t *testing.T) {
	cfg, err := FromLookup(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("FromLookup() error = %v", err)
	}

	if cfg.Environment != EnvironmentDevelopment {
		t.Fatalf("Environment = %q, want %q", cfg.Environment, EnvironmentDevelopment)
	}
	if cfg.HTTP.Address != ":8080" {
		t.Fatalf("HTTP.Address = %q, want :8080", cfg.HTTP.Address)
	}
	if cfg.HTTP.MaxRequestBytes != 16<<20 {
		t.Fatalf("HTTP.MaxRequestBytes = %d, want %d", cfg.HTTP.MaxRequestBytes, int64(16<<20))
	}
	if cfg.Embedding.Provider != "deterministic" || cfg.Embedding.Dimension != 128 {
		t.Fatalf("unexpected embedding defaults: %+v", cfg.Embedding)
	}
	if cfg.Auth.Mode != "disabled" {
		t.Fatalf("Auth.Mode = %q, want disabled", cfg.Auth.Mode)
	}
	if cfg.S3.Prefix != "tenants" || cfg.Worker.LeaseDuration != 5*time.Minute ||
		cfg.Worker.RetryBase != 5*time.Second || cfg.Worker.RetryMaximum != 5*time.Minute {
		t.Fatalf("unexpected storage/worker defaults: S3=%+v Worker=%+v", cfg.S3, cfg.Worker)
	}
}

func TestFromLookupOverridesValues(t *testing.T) {
	values := map[string]string{
		"MSS_KNOWLEDGE_SERVICE_NAME":                 "gateway",
		"MSS_KNOWLEDGE_ENVIRONMENT":                  "test",
		"MSS_KNOWLEDGE_HTTP_ADDRESS":                 "127.0.0.1:9090",
		"MSS_KNOWLEDGE_HTTP_READ_HEADER_TIMEOUT":     "2s",
		"MSS_KNOWLEDGE_HTTP_READ_TIMEOUT":            "3s",
		"MSS_KNOWLEDGE_HTTP_WRITE_TIMEOUT":           "4s",
		"MSS_KNOWLEDGE_HTTP_IDLE_TIMEOUT":            "5s",
		"MSS_KNOWLEDGE_HTTP_MAX_REQUEST_BYTES":       "2048",
		"MSS_KNOWLEDGE_DATABASE_URL":                 "postgres://example/test",
		"MSS_KNOWLEDGE_DATABASE_MAX_CONNECTIONS":     "24",
		"MSS_KNOWLEDGE_DATABASE_MIN_CONNECTIONS":     "2",
		"MSS_KNOWLEDGE_REDIS_ADDRESS":                "redis.example:6380",
		"MSS_KNOWLEDGE_REDIS_USERNAME":               "app",
		"MSS_KNOWLEDGE_REDIS_PASSWORD":               " secret ",
		"MSS_KNOWLEDGE_REDIS_DATABASE":               "3",
		"MSS_KNOWLEDGE_REDIS_TLS":                    "true",
		"MSS_KNOWLEDGE_REDIS_INDEX_NAME":             "chunks-v2",
		"MSS_KNOWLEDGE_REDIS_KEY_PREFIX":             "test:chunk:",
		"MSS_KNOWLEDGE_S3_PREFIX":                    "objects/root",
		"MSS_KNOWLEDGE_AUTH_MODE":                    "STATIC",
		"MSS_KNOWLEDGE_STATIC_TOKEN":                 " bearer-secret ",
		"MSS_KNOWLEDGE_STATIC_TENANT_ID":             "tenant_1",
		"MSS_KNOWLEDGE_STATIC_PRINCIPAL_ID":          "principal_1",
		"MSS_KNOWLEDGE_STATIC_SCOPES":                "knowledge.search, knowledge.read,knowledge.search",
		"MSS_KNOWLEDGE_EMBEDDING_MODEL":              "test-hash",
		"MSS_KNOWLEDGE_EMBEDDING_DIMENSION":          "256",
		"MSS_KNOWLEDGE_SEARCH_MAX_TOP_K":             "40",
		"MSS_KNOWLEDGE_SEARCH_CANDIDATE_MULTIPLIER":  "4",
		"MSS_KNOWLEDGE_SEARCH_MAX_HITS_PER_DOCUMENT": "2",
		"MSS_KNOWLEDGE_WORKER_POLL_INTERVAL":         "750ms",
		"MSS_KNOWLEDGE_WORKER_LEASE_DURATION":        "45s",
		"MSS_KNOWLEDGE_WORKER_ID":                    "worker-test",
		"MSS_KNOWLEDGE_WORKER_RETRY_BASE":            "3s",
		"MSS_KNOWLEDGE_WORKER_RETRY_MAXIMUM":         "2m",
		"MSS_KNOWLEDGE_SHUTDOWN_TIMEOUT":             "6s",
		"MSS_KNOWLEDGE_LOG_LEVEL":                    "DEBUG",
	}

	cfg, err := FromLookup(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("FromLookup() error = %v", err)
	}

	if cfg.ServiceName != "gateway" || cfg.Environment != EnvironmentTest {
		t.Fatalf("unexpected identity config: %+v", cfg)
	}
	if cfg.HTTP.Address != "127.0.0.1:9090" {
		t.Fatalf("HTTP.Address = %q", cfg.HTTP.Address)
	}
	if cfg.HTTP.ReadHeaderTimeout != 2*time.Second || cfg.HTTP.WriteTimeout != 4*time.Second {
		t.Fatalf("unexpected HTTP timeouts: %+v", cfg.HTTP)
	}
	if cfg.HTTP.MaxRequestBytes != 2048 {
		t.Fatalf("HTTP.MaxRequestBytes = %d", cfg.HTTP.MaxRequestBytes)
	}
	if cfg.Database.URL == "" || cfg.Database.MaxConnections != 24 || cfg.Database.MinConnections != 2 {
		t.Fatalf("unexpected database config: %+v", cfg.Database)
	}
	if cfg.Redis.Address != "redis.example:6380" || cfg.Redis.Database != 3 || !cfg.Redis.TLS {
		t.Fatalf("unexpected Redis config: %+v", cfg.Redis)
	}
	if cfg.Redis.Password != " secret " {
		t.Fatalf("Redis password was unexpectedly normalized")
	}
	if cfg.S3.Prefix != "objects/root" {
		t.Fatalf("unexpected S3 prefix: %+v", cfg.S3)
	}
	if cfg.Auth.Mode != "static" || cfg.Auth.StaticToken != " bearer-secret " || len(cfg.Auth.Scopes) != 2 {
		t.Fatalf("unexpected auth config: %+v", cfg.Auth)
	}
	if cfg.Embedding.Model != "test-hash" || cfg.Embedding.Dimension != 256 {
		t.Fatalf("unexpected embedding config: %+v", cfg.Embedding)
	}
	if cfg.Search.MaxTopK != 40 || cfg.Search.CandidateMultiplier != 4 || cfg.Search.MaxHitsPerDocument != 2 {
		t.Fatalf("unexpected search config: %+v", cfg.Search)
	}
	if cfg.Worker.ID != "worker-test" || cfg.Worker.PollInterval != 750*time.Millisecond ||
		cfg.Worker.LeaseDuration != 45*time.Second || cfg.Worker.RetryBase != 3*time.Second ||
		cfg.Worker.RetryMaximum != 2*time.Minute {
		t.Fatalf("unexpected worker config: %+v", cfg.Worker)
	}
	if cfg.ShutdownTimeout != 6*time.Second || cfg.LogLevel != "debug" {
		t.Fatalf("unexpected process config: %+v", cfg)
	}
}

func TestFromLookupRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "environment", key: "MSS_KNOWLEDGE_ENVIRONMENT", value: "unknown"},
		{name: "address", key: "MSS_KNOWLEDGE_HTTP_ADDRESS", value: "8080"},
		{name: "duration", key: "MSS_KNOWLEDGE_HTTP_READ_TIMEOUT", value: "soon"},
		{name: "bytes", key: "MSS_KNOWLEDGE_HTTP_MAX_REQUEST_BYTES", value: "many"},
		{name: "database bounds", key: "MSS_KNOWLEDGE_DATABASE_MAX_CONNECTIONS", value: "0"},
		{name: "Redis database", key: "MSS_KNOWLEDGE_REDIS_DATABASE", value: "-1"},
		{name: "embedding dimension", key: "MSS_KNOWLEDGE_EMBEDDING_DIMENSION", value: "4"},
		{name: "auth mode", key: "MSS_KNOWLEDGE_AUTH_MODE", value: "magic"},
		{name: "log level", key: "MSS_KNOWLEDGE_LOG_LEVEL", value: "trace"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FromLookup(func(key string) (string, bool) {
				if key == tt.key {
					return tt.value, true
				}
				return "", false
			})
			if err == nil {
				t.Fatal("FromLookup() error = nil, want non-nil")
			}
		})
	}
}

func TestValidateGatewayRequiresDependenciesAndAuthentication(t *testing.T) {
	cfg, err := FromLookup(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("FromLookup() error = %v", err)
	}
	if err := cfg.ValidateGateway(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("ValidateGateway() error = %v", err)
	}

	cfg.Database.URL = "postgres://example/test"
	cfg.Redis.Address = "redis:6379"
	if err := cfg.ValidateGateway(); err == nil || !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("ValidateGateway() error = %v", err)
	}

	cfg.Auth.Mode = "static"
	cfg.Auth.StaticToken = "token"
	cfg.Auth.TenantID = "tenant_1"
	cfg.Auth.PrincipalID = "principal_1"
	cfg.S3 = S3Config{
		Endpoint:          "http://127.0.0.1:9000",
		Bucket:            "mss-knowledge",
		Prefix:            "tenants",
		AccessKeyID:       "access-key",
		SecretAccessKey:   "secret-key",
		PathStyle:         true,
		RequireVersioning: true,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if err := cfg.ValidateGateway(); err != nil {
		t.Fatalf("ValidateGateway() error = %v", err)
	}
}

func TestValidateRejectsStaticAuthenticationInProduction(t *testing.T) {
	cfg, err := FromLookup(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("FromLookup() error = %v", err)
	}
	cfg.Environment = EnvironmentProduction
	cfg.Auth = AuthConfig{
		Mode:        "static",
		StaticToken: "token",
		TenantID:    "tenant_1",
		PrincipalID: "principal_1",
		Scopes:      []string{"knowledge.search"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want production static auth rejection")
	}
}

func TestValidateObjectStorage(t *testing.T) {
	cfg, err := FromLookup(func(key string) (string, bool) {
		values := map[string]string{
			"MSS_KNOWLEDGE_S3_ENDPOINT":          "http://127.0.0.1:9000",
			"MSS_KNOWLEDGE_S3_BUCKET":            "mss-knowledge",
			"MSS_KNOWLEDGE_S3_ACCESS_KEY_ID":     "access-key",
			"MSS_KNOWLEDGE_S3_SECRET_ACCESS_KEY": "secret-key",
		}
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("FromLookup() error = %v", err)
	}
	if err := cfg.ValidateObjectStorage(); err != nil {
		t.Fatalf("ValidateObjectStorage() error = %v", err)
	}
	if !cfg.S3.PathStyle || !cfg.S3.RequireVersioning {
		t.Fatalf("unexpected S3 defaults: %+v", cfg.S3)
	}
}
