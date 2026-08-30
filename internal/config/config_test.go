package config

import (
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
	if cfg.HTTP.MaxRequestBytes != 1<<20 {
		t.Fatalf("HTTP.MaxRequestBytes = %d, want %d", cfg.HTTP.MaxRequestBytes, int64(1<<20))
	}
}

func TestFromLookupOverridesValues(t *testing.T) {
	values := map[string]string{
		"MSS_KNOWLEDGE_SERVICE_NAME":             "gateway",
		"MSS_KNOWLEDGE_ENVIRONMENT":              "test",
		"MSS_KNOWLEDGE_HTTP_ADDRESS":             "127.0.0.1:9090",
		"MSS_KNOWLEDGE_HTTP_READ_HEADER_TIMEOUT": "2s",
		"MSS_KNOWLEDGE_HTTP_READ_TIMEOUT":        "3s",
		"MSS_KNOWLEDGE_HTTP_WRITE_TIMEOUT":       "4s",
		"MSS_KNOWLEDGE_HTTP_IDLE_TIMEOUT":        "5s",
		"MSS_KNOWLEDGE_HTTP_MAX_REQUEST_BYTES":   "2048",
		"MSS_KNOWLEDGE_SHUTDOWN_TIMEOUT":         "6s",
		"MSS_KNOWLEDGE_LOG_LEVEL":                "DEBUG",
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
