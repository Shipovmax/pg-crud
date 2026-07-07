package repository

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker"
	"golang.org/x/sync/singleflight"

	"pg-crud/logging"
	"pg-crud/metrics"
)

// cacheOpTimeout bounds every individual Redis round-trip. It is
// deliberately far below the Postgres query timeout so a degraded cache
// never becomes the slowest leg of a request.
const cacheOpTimeout = 300 * time.Millisecond

// BreakerConfig configures the circuit breaker guarding Redis operations.
// Values come from config.Load(), never hardcoded at the call site.
type BreakerConfig struct {
	// Threshold is the number of consecutive Redis failures that trips
	// the breaker into the Open state.
	Threshold uint32
	// Cooldown is how long the breaker stays Open before allowing a
	// single probe request through (Half-Open).
	Cooldown time.Duration
}

// cachedUserRepository is a cache-aside decorator around UserRepository.
// It is the only place in the codebase aware of Redis; pgUserRepository
// and the HTTP handlers never see the cache.
type cachedUserRepository struct {
	next    UserRepository
	redis   *redis.Client
	breaker *gobreaker.CircuitBreaker
	ttl     time.Duration
	sf      singleflight.Group
	metrics *metrics.CacheMetrics
}

// NewCachedUserRepository wraps next with a Redis cache-aside layer. GetByID
// hits are served from Redis; misses collapse via singleflight into a
// single Postgres call. Update/Delete invalidate the cache after next
// confirms the write. Any Redis failure fails open onto next.
func NewCachedUserRepository(next UserRepository, rdb *redis.Client, ttl time.Duration, breakerCfg BreakerConfig, m *metrics.CacheMetrics) UserRepository {
	breaker := gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:    "redis",
		Timeout: breakerCfg.Cooldown,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= breakerCfg.Threshold
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			slog.Default().Warn("circuit breaker state change",
				"breaker", name, "from", from.String(), "to", to.String())
			m.BreakerState.Set(float64(to))
		},
	})

	return &cachedUserRepository{
		next:    next,
		redis:   rdb,
		breaker: breaker,
		ttl:     ttl,
		metrics: m,
	}
}

func (r *cachedUserRepository) Create(ctx context.Context, name, email string) (*User, error) {
	return r.next.Create(ctx, name, email)
}

func (r *cachedUserRepository) GetByID(ctx context.Context, id int64) (*User, error) {
	key := userCacheKey(id)

	data, err := r.getFromCache(ctx, key)
	switch {
	case err == nil:
		var u User
		unmarshalErr := json.Unmarshal(data, &u)
		if unmarshalErr == nil {
			r.metrics.Hits.Inc()
			return &u, nil
		}
		logging.FromContext(ctx).Warn("cache unmarshal failed", "user_id", id, "error", unmarshalErr)
		r.metrics.Errors.Inc()
	case errors.Is(err, redis.Nil):
		r.metrics.Misses.Inc()
	default:
		// Covers gobreaker.ErrOpenState/ErrTooManyRequests and any
		// transport/timeout error alike: the cache path failed, fall
		// through to Postgres (fail-open).
		logging.FromContext(ctx).Warn("cache get failed", "user_id", id, "error", err)
		r.metrics.Errors.Inc()
	}

	v, err, _ := r.sf.Do(key, func() (any, error) {
		u, err := r.next.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		r.setAsync(u)
		return u, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*User), nil
}

func (r *cachedUserRepository) List(ctx context.Context, limit, offset int) ([]*User, error) {
	return r.next.List(ctx, limit, offset)
}

func (r *cachedUserRepository) Update(ctx context.Context, id int64, name, email string, version int64) (*User, error) {
	u, err := r.next.Update(ctx, id, name, email, version) // source of truth first
	if err != nil {
		return nil, err
	}
	r.invalidate(ctx, id) // then the cache; a failed DEL just logs, TTL cleans up
	return u, nil
}

func (r *cachedUserRepository) Delete(ctx context.Context, id int64) error {
	if err := r.next.Delete(ctx, id); err != nil {
		return err
	}
	r.invalidate(ctx, id)
	return nil
}

// getFromCache reads key from Redis through the circuit breaker. The
// returned error is either redis.Nil (true cache miss), a gobreaker
// sentinel (breaker refused to even attempt the call), or a transport
// error — callers distinguish redis.Nil from everything else.
func (r *cachedUserRepository) getFromCache(ctx context.Context, key string) ([]byte, error) {
	v, err := r.breaker.Execute(func() (any, error) {
		cacheCtx, cancel := context.WithTimeout(ctx, cacheOpTimeout)
		defer cancel()
		return r.redis.Get(cacheCtx, key).Bytes()
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

// invalidate deletes the cached entry for id. It runs on the request path
// (after the write commits) but is bounded by cacheOpTimeout via the
// breaker, so a degraded Redis adds at most one bounded delay to a
// write request instead of hanging it.
func (r *cachedUserRepository) invalidate(ctx context.Context, id int64) {
	_, err := r.breaker.Execute(func() (any, error) {
		cacheCtx, cancel := context.WithTimeout(ctx, cacheOpTimeout)
		defer cancel()
		return nil, r.redis.Del(cacheCtx, userCacheKey(id)).Err()
	})
	if err != nil {
		logging.FromContext(ctx).Warn("cache invalidate failed", "user_id", id, "error", err)
		r.metrics.Errors.Inc()
	}
}

// setAsync warms the cache in the background so a slow or degraded Redis
// never adds latency to the response already being sent to the client.
// The goroutine carries its own bounded context (cacheOpTimeout), detached
// from the request context, so it always terminates on its own and never
// leaks regardless of client cancellation.
func (r *cachedUserRepository) setAsync(u *User) {
	payload, err := json.Marshal(u)
	if err != nil {
		slog.Default().Error("cache marshal failed", "user_id", u.ID, "error", err)
		r.metrics.Errors.Inc()
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), cacheOpTimeout)
		defer cancel()

		_, err := r.breaker.Execute(func() (any, error) {
			return nil, r.redis.Set(ctx, userCacheKey(u.ID), payload, r.jitteredTTL()).Err()
		})
		if err != nil {
			// Detached from the request, so no trace_id here by design.
			slog.Default().Warn("cache set failed", "user_id", u.ID, "error", err)
			r.metrics.Errors.Inc()
		}
	}()
}

// jitteredTTL returns ttl scaled by a uniform random factor in [0.9, 1.1),
// so keys warmed in the same instant don't all expire in the same instant
// and stampede Postgres together.
func (r *cachedUserRepository) jitteredTTL() time.Duration {
	jitter := 0.9 + rand.Float64()*0.2
	return time.Duration(float64(r.ttl) * jitter)
}

func userCacheKey(id int64) string {
	return "user:" + strconv.FormatInt(id, 10)
}
