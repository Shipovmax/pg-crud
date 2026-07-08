package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds application settings loaded from environment variables.
type Config struct {
	DatabaseURL  string
	ServerAddr   string
	PoolMaxConns int32

	RedisAddr         string
	RedisPoolSize     int
	RedisMinIdleConns int
	CacheTTL          time.Duration
	BreakerThreshold  uint32
	BreakerCooldown   time.Duration

	// APIKeys is the set of accepted X-API-Key values. Empty means
	// authentication is disabled — acceptable for local development,
	// the caller (main) must log this loudly rather than fail silently.
	APIKeys map[string]struct{}

	// OTelExporterEndpoint is the OTLP/gRPC collector address. Empty
	// falls back to a stdout exporter, so tracing is observable locally
	// without standing up a collector.
	OTelExporterEndpoint string
}

// Load reads configuration from environment variables and validates it.
func Load() (*Config, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, errors.New("DATABASE_URL is required")
	}

	addr := os.Getenv("SERVER_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	poolMaxConns := int32(10)
	if v := os.Getenv("POOL_MAX_CONNS"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("POOL_MAX_CONNS must be a positive integer: %q", v)
		}
		poolMaxConns = int32(n)
	}

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	redisPoolSize, err := parsePositiveIntEnv("REDIS_POOL_SIZE", 10)
	if err != nil {
		return nil, err
	}

	redisMinIdleConns, err := parsePositiveIntEnv("REDIS_MIN_IDLE_CONNS", 2)
	if err != nil {
		return nil, err
	}

	cacheTTLSeconds, err := parsePositiveIntEnv("CACHE_TTL_SECONDS", 60)
	if err != nil {
		return nil, err
	}

	breakerThreshold, err := parsePositiveIntEnv("BREAKER_THRESHOLD", 5)
	if err != nil {
		return nil, err
	}

	breakerCooldownSeconds, err := parsePositiveIntEnv("BREAKER_COOLDOWN_SECONDS", 30)
	if err != nil {
		return nil, err
	}

	apiKeys := make(map[string]struct{})
	if v := os.Getenv("API_KEYS"); v != "" {
		for key := range strings.SplitSeq(v, ",") {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			apiKeys[key] = struct{}{}
		}
	}

	return &Config{
		DatabaseURL:  dsn,
		ServerAddr:   addr,
		PoolMaxConns: poolMaxConns,

		RedisAddr:         redisAddr,
		RedisPoolSize:     redisPoolSize,
		RedisMinIdleConns: redisMinIdleConns,
		CacheTTL:          time.Duration(cacheTTLSeconds) * time.Second,
		BreakerThreshold:  uint32(breakerThreshold),
		BreakerCooldown:   time.Duration(breakerCooldownSeconds) * time.Second,

		APIKeys:              apiKeys,
		OTelExporterEndpoint: os.Getenv("OTEL_EXPORTER_ENDPOINT"),
	}, nil
}

// parsePositiveIntEnv reads a positive integer from the named environment
// variable, falling back to def when unset.
func parsePositiveIntEnv(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer: %q", name, v)
	}
	return n, nil
}
