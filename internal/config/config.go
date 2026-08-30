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

// Config contains process-level configuration shared by the foundation binaries.
type Config struct {
	ServiceName     string
	Environment     Environment
	HTTP            HTTPConfig
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
			MaxRequestBytes:   1 << 20,
		},
		ShutdownTimeout: 15 * time.Second,
		LogLevel:        "info",
	}

	cfg.ServiceName = stringValue(lookup, "MSS_KNOWLEDGE_SERVICE_NAME", cfg.ServiceName)
	cfg.Environment = Environment(stringValue(lookup, "MSS_KNOWLEDGE_ENVIRONMENT", string(cfg.Environment)))
	cfg.HTTP.Address = stringValue(lookup, "MSS_KNOWLEDGE_HTTP_ADDRESS", cfg.HTTP.Address)
	cfg.LogLevel = strings.ToLower(stringValue(lookup, "MSS_KNOWLEDGE_LOG_LEVEL", cfg.LogLevel))

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
	if cfg.HTTP.MaxRequestBytes, err = int64Value(lookup, "MSS_KNOWLEDGE_HTTP_MAX_REQUEST_BYTES", cfg.HTTP.MaxRequestBytes); err != nil {
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
	} {
		if value <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}

	if c.HTTP.MaxRequestBytes <= 0 {
		return fmt.Errorf("HTTP max request bytes must be positive")
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("unsupported log level %q", c.LogLevel)
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
