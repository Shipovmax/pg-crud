package config

import (
	"errors"
	"os"
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

	return &Config{
		DatabaseURL:  dsn,
		ServerAddr:   addr,
		PoolMaxConns: 10,
	}, nil
}
