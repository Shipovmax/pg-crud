package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	redisDialTimeout  = 2 * time.Second
	redisReadTimeout  = 500 * time.Millisecond
	redisWriteTimeout = 500 * time.Millisecond
	redisPingTimeout  = 2 * time.Second
)

// RedisConfig holds the connection parameters for the cache client, all
// sourced from config.Load — nothing here is hardcoded at the call site.
type RedisConfig struct {
	Addr         string
	PoolSize     int
	MinIdleConns int
}

// NewRedisClient builds a Redis client bounded by explicit dial/read/write
// timeouts, so a stalled Redis never blocks a handler goroutine
// indefinitely, and verifies connectivity with a single bounded Ping.
func NewRedisClient(ctx context.Context, cfg RedisConfig) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		PoolSize:     cfg.PoolSize,
		MinIdleConns: cfg.MinIdleConns,
		DialTimeout:  redisDialTimeout,
		ReadTimeout:  redisReadTimeout,
		WriteTimeout: redisWriteTimeout,
	})

	pingCtx, cancel := context.WithTimeout(ctx, redisPingTimeout)
	defer cancel()

	if err := client.Ping(pingCtx).Err(); err != nil {
		closeErr := client.Close()
		return nil, fmt.Errorf("redis ping: %w", errors.Join(err, closeErr))
	}
	return client, nil
}
