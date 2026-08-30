package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment identifies the runtime environment.
type Environment string

const (
	EnvironmentDevelopment Environment = "development"
	EnvironmentTest        Environment = "test"
	EnvironmentStaging     Environment = "staging"
	EnvironmentProduction  Environment = "production"
)

// Config contains process and adapter configuration shared by the binaries.
type Config struct {
	ServiceName     string
	Environment     Environment
	HTTP            HTTPConfig
	Database        DatabaseConfig
	Redis           RedisConfig
	Auth            AuthConfig
	Embedding       EmbeddingConfig
	Search          SearchConfig
	Worker          WorkerConfig
	ShutdownTimeout time.Duration
	LogLevel        string
}

// HTTPConfig controls the public HTTP server.
type HTTPConfig struct {
	Address           string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxRequestBytes   int64
}

// DatabaseConfig controls the PostgreSQL control-plane adapter.
type DatabaseConfig struct {
	URL            string
	MaxConnections int32
	MinConnections int32
}

// RedisConfig controls the Redis Query Engine projection.
type RedisConfig struct {
	Address   string
	Username  string
	Password  string
	Database  int
	TLS       bool
	IndexName string
	KeyPrefix string
}

// AuthConfig controls the first-version bearer-token resolver.
type AuthConfig struct {
	Mode        string
	StaticToken string
	TenantID    string
	PrincipalID string
	Scopes      []string
}

// EmbeddingConfig selects a query and document embedding provider.
type EmbeddingConfig struct {
	Provider  string
	Model     string
	Dimension int
}

// SearchConfig controls bounded result generation.
type SearchConfig struct {
	MaxTopK             int
	CandidateMultiplier int
	MaxHitsPerDocument  int
}

// WorkerConfig controls the durable ingestion poller.
type WorkerConfig struct {
	PollInterval  time.Duration
	LeaseDuration time.Duration
}

// LookupEnv is compatible with os.LookupEnv and makes loading deterministic in tests.
type LookupEnv func(string) (string, bool)

// Load reads configuration from the process environment.
func Load() (Config, error) {
	return FromLookup(os.LookupEnv)
}

// FromLookup reads configuration from the supplied lookup function.
func FromLookup(lookup LookupEnv) (Config, error) {
	cfg := Config{
		ServiceName: "mss-knowledge",
		Environment: EnvironmentDevelopment,
		HTTP: HTTPConfig{
			Address:           ":8080",
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxRequestBytes:   16 << 20,
		},
		Database: DatabaseConfig{
			MaxConnections: 10,
			MinConnections: 1,
		},
		Redis: RedisConfig{
			Database:  0,
			IndexName: "mss-knowledge-chunks-v1",
			KeyPrefix: "mk:chunk:",
		},
		Auth: AuthConfig{
			Mode:   "disabled",
			Scopes: []string{"knowledge.search", "knowledge.read"},
		},
		Embedding: EmbeddingConfig{
			Provider:  "deterministic",
			Model:     "deterministic-hash-v1",
			Dimension: 128,
		},
		Search: SearchConfig{
			MaxTopK:             20,
			CandidateMultiplier: 5,
			MaxHitsPerDocument:  3,
		},
		Worker: WorkerConfig{
			PollInterval:  2 * time.Second,
			LeaseDuration: 30 * time.Second,
		},
		ShutdownTimeout: 15 * time.Second,
		LogLevel:        "info",
	}

	cfg.ServiceName = stringValue(lookup, "MSS_KNOWLEDGE_SERVICE_NAME", cfg.ServiceName)
	cfg.Environment = Environment(stringValue(lookup, "MSS_KNOWLEDGE_ENVIRONMENT", string(cfg.Environment)))
	cfg.HTTP.Address = stringValue(lookup, "MSS_KNOWLEDGE_HTTP_ADDRESS", cfg.HTTP.Address)
	cfg.LogLevel = strings.ToLower(stringValue(lookup, "MSS_KNOWLEDGE_LOG_LEVEL", cfg.LogLevel))
	cfg.Database.URL = stringValue(lookup, "MSS_KNOWLEDGE_DATABASE_URL", cfg.Database.URL)
	cfg.Redis.Address = stringValue(lookup, "MSS_KNOWLEDGE_REDIS_ADDRESS", cfg.Redis.Address)
	cfg.Redis.Username = stringValue(lookup, "MSS_KNOWLEDGE_REDIS_USERNAME", cfg.Redis.Username)
	cfg.Redis.Password = rawStringValue(lookup, "MSS_KNOWLEDGE_REDIS_PASSWORD", cfg.Redis.Password)
	cfg.Redis.IndexName = stringValue(lookup, "MSS_KNOWLEDGE_REDIS_INDEX_NAME", cfg.Redis.IndexName)
	cfg.Redis.KeyPrefix = stringValue(lookup, "MSS_KNOWLEDGE_REDIS_KEY_PREFIX", cfg.Redis.KeyPrefix)
	cfg.Auth.Mode = strings.ToLower(stringValue(lookup, "MSS_KNOWLEDGE_AUTH_MODE", cfg.Auth.Mode))
	cfg.Auth.StaticToken = rawStringValue(lookup, "MSS_KNOWLEDGE_STATIC_TOKEN", cfg.Auth.StaticToken)
	cfg.Auth.TenantID = stringValue(lookup, "MSS_KNOWLEDGE_STATIC_TENANT_ID", cfg.Auth.TenantID)
	cfg.Auth.PrincipalID = stringValue(lookup, "MSS_KNOWLEDGE_STATIC_PRINCIPAL_ID", cfg.Auth.PrincipalID)
	cfg.Auth.Scopes = csvValue(lookup, "MSS_KNOWLEDGE_STATIC_SCOPES", cfg.Auth.Scopes)
	cfg.Embedding.Provider = strings.ToLower(stringValue(lookup, "MSS_KNOWLEDGE_EMBEDDING_PROVIDER", cfg.Embedding.Provider))
	cfg.Embedding.Model = stringValue(lookup, "MSS_KNOWLEDGE_EMBEDDING_MODEL", cfg.Embedding.Model)

	var err error
	if cfg.HTTP.ReadHeaderTimeout, err = durationValue(lookup, "MSS_KNOWLEDGE_HTTP_READ_HEADER_TIMEOUT", cfg.HTTP.ReadHeaderTimeout); err != nil {
		return Config{}, err
	}
	if cfg.HTTP.ReadTimeout, err = durationValue(lookup, "MSS_KNOWLEDGE_HTTP_READ_TIMEOUT", cfg.HTTP.ReadTimeout); err != nil {
		return Config{}, err
	}
	if cfg.HTTP.WriteTimeout, err = durationValue(lookup, "MSS_KNOWLEDGE_HTTP_WRITE_TIMEOUT", cfg.HTTP.WriteTimeout); err != nil {
		return Config{}, err
	}
	if cfg.HTTP.IdleTimeout, err = durationValue(lookup, "MSS_KNOWLEDGE_HTTP_IDLE_TIMEOUT", cfg.HTTP.IdleTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = durationValue(lookup, "MSS_KNOWLEDGE_SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout); err != nil {
		return Config{}, err
	}
	if cfg.Worker.PollInterval, err = durationValue(lookup, "MSS_KNOWLEDGE_WORKER_POLL_INTERVAL", cfg.Worker.PollInterval); err != nil {
		return Config{}, err
	}
	if cfg.Worker.LeaseDuration, err = durationValue(lookup, "MSS_KNOWLEDGE_WORKER_LEASE_DURATION", cfg.Worker.LeaseDuration); err != nil {
		return Config{}, err
	}
	if cfg.HTTP.MaxRequestBytes, err = int64Value(lookup, "MSS_KNOWLEDGE_HTTP_MAX_REQUEST_BYTES", cfg.HTTP.MaxRequestBytes); err != nil {
		return Config{}, err
	}
	if cfg.Database.MaxConnections, err = int32Value(lookup, "MSS_KNOWLEDGE_DATABASE_MAX_CONNECTIONS", cfg.Database.MaxConnections); err != nil {
		return Config{}, err
	}
	if cfg.Database.MinConnections, err = int32Value(lookup, "MSS_KNOWLEDGE_DATABASE_MIN_CONNECTIONS", cfg.Database.MinConnections); err != nil {
		return Config{}, err
	}
	if cfg.Redis.Database, err = intValue(lookup, "MSS_KNOWLEDGE_REDIS_DATABASE", cfg.Redis.Database); err != nil {
		return Config{}, err
	}
	if cfg.Redis.TLS, err = boolValue(lookup, "MSS_KNOWLEDGE_REDIS_TLS", cfg.Redis.TLS); err != nil {
		return Config{}, err
	}
	if cfg.Embedding.Dimension, err = intValue(lookup, "MSS_KNOWLEDGE_EMBEDDING_DIMENSION", cfg.Embedding.Dimension); err != nil {
		return Config{}, err
	}
	if cfg.Search.MaxTopK, err = intValue(lookup, "MSS_KNOWLEDGE_SEARCH_MAX_TOP_K", cfg.Search.MaxTopK); err != nil {
		return Config{}, err
	}
	if cfg.Search.CandidateMultiplier, err = intValue(lookup, "MSS_KNOWLEDGE_SEARCH_CANDIDATE_MULTIPLIER", cfg.Search.CandidateMultiplier); err != nil {
		return Config{}, err
	}
	if cfg.Search.MaxHitsPerDocument, err = intValue(lookup, "MSS_KNOWLEDGE_SEARCH_MAX_HITS_PER_DOCUMENT", cfg.Search.MaxHitsPerDocument); err != nil {
		return Config{}, err
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks configuration invariants before a process starts listening.
func (c Config) Validate() error {
	if strings.TrimSpace(c.ServiceName) == "" {
		return fmt.Errorf("service name must not be empty")
	}

	switch c.Environment {
	case EnvironmentDevelopment, EnvironmentTest, EnvironmentStaging, EnvironmentProduction:
	default:
		return fmt.Errorf("unsupported environment %q", c.Environment)
	}

	if err := validateAddress(c.HTTP.Address); err != nil {
		return fmt.Errorf("invalid HTTP address: %w", err)
	}

	for name, value := range map[string]time.Duration{
		"HTTP read header timeout": c.HTTP.ReadHeaderTimeout,
		"HTTP read timeout":        c.HTTP.ReadTimeout,
		"HTTP write timeout":       c.HTTP.WriteTimeout,
		"HTTP idle timeout":        c.HTTP.IdleTimeout,
		"shutdown timeout":         c.ShutdownTimeout,
		"worker poll interval":     c.Worker.PollInterval,
		"worker lease duration":    c.Worker.LeaseDuration,
	} {
		if value <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}

	if c.HTTP.MaxRequestBytes <= 0 {
		return fmt.Errorf("HTTP max request bytes must be positive")
	}
	if c.Database.MaxConnections <= 0 || c.Database.MinConnections < 0 || c.Database.MinConnections > c.Database.MaxConnections {
		return fmt.Errorf("database connection bounds are invalid")
	}
	if c.Redis.Database < 0 {
		return fmt.Errorf("Redis database must be non-negative")
	}
	if strings.TrimSpace(c.Redis.IndexName) == "" || strings.TrimSpace(c.Redis.KeyPrefix) == "" {
		return fmt.Errorf("Redis index name and key prefix must not be empty")
	}
	if c.Embedding.Dimension < 16 || c.Embedding.Dimension > 4096 {
		return fmt.Errorf("embedding dimension must be between 16 and 4096")
	}
	if c.Embedding.Provider != "deterministic" {
		return fmt.Errorf("unsupported embedding provider %q", c.Embedding.Provider)
	}
	if c.Search.MaxTopK <= 0 || c.Search.MaxTopK > 100 || c.Search.CandidateMultiplier <= 0 || c.Search.CandidateMultiplier > 20 || c.Search.MaxHitsPerDocument <= 0 {
		return fmt.Errorf("search bounds are invalid")
	}

	switch c.Auth.Mode {
	case "disabled", "static":
	default:
		return fmt.Errorf("unsupported auth mode %q", c.Auth.Mode)
	}
	if c.Auth.Mode == "static" {
		if strings.TrimSpace(c.Auth.StaticToken) == "" || c.Auth.TenantID == "" || c.Auth.PrincipalID == "" || len(c.Auth.Scopes) == 0 {
			return fmt.Errorf("static auth requires token, tenant, principal, and scopes")
		}
		if c.Environment == EnvironmentProduction {
			return fmt.Errorf("static auth is not permitted in production")
		}
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("unsupported log level %q", c.LogLevel)
	}
	return nil
}

// ValidateGateway checks dependencies required by the public gateway.
func (c Config) ValidateGateway() error {
	if strings.TrimSpace(c.Database.URL) == "" {
		return fmt.Errorf("MSS_KNOWLEDGE_DATABASE_URL is required")
	}
	if strings.TrimSpace(c.Redis.Address) == "" {
		return fmt.Errorf("MSS_KNOWLEDGE_REDIS_ADDRESS is required")
	}
	if c.Auth.Mode == "disabled" {
		return fmt.Errorf("authentication must be configured")
	}
	return nil
}

func validateAddress(address string) error {
	if strings.TrimSpace(address) == "" {
		return fmt.Errorf("address must not be empty")
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("invalid port %q", port)
	}
	if portNumber < 0 || portNumber > 65535 {
		return fmt.Errorf("port out of range")
	}
	return nil
}

func stringValue(lookup LookupEnv, key, fallback string) string {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func rawStringValue(lookup LookupEnv, key, fallback string) string {
	value, ok := lookup(key)
	if !ok || value == "" {
		return fallback
	}
	return value
}

func csvValue(lookup LookupEnv, key string, fallback []string) []string {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return append([]string(nil), fallback...)
	}
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

func durationValue(lookup LookupEnv, key string, fallback time.Duration) (time.Duration, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}

func int64Value(lookup LookupEnv, key string, fallback int64) (int64, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}

func int32Value(lookup LookupEnv, key string, fallback int32) (int32, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return int32(parsed), nil
}

func intValue(lookup LookupEnv, key string, fallback int) (int, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}

func boolValue(lookup LookupEnv, key string, fallback bool) (bool, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}
