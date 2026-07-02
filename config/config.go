package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

// Config holds application settings loaded from environment variables.
type Config struct {
	DatabaseURL  string
	ServerAddr   string
	PoolMaxConns int32
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

	return &Config{
		DatabaseURL:  dsn,
		ServerAddr:   addr,
		PoolMaxConns: poolMaxConns,
	}, nil
}
