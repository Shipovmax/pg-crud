# Task #9 — Redis Cache Layer (pg-crud)

## Goal

Add a cache-aside layer on Redis on top of `pg-crud`, without touching `pgUserRepository` and without leaking the abstraction into `handler`. The learning goal is not "bolt on Redis" but to work through the real problems of caching: **cache stampede**, **invalidation after write**, **fail-open degradation**, and a **circuit breaker** on an external dependency that can go down under load. The task is blocked by unresolved tech debt — `List()` without pagination has no right to be cached until it is bounded by volume.

---

## Acceptance Criteria

- [ ] `List()` migrated to `LIMIT`/`OFFSET` (or keyset pagination by `id`) — **prod blocker**, the List cache item makes no sense without this
- [ ] `GetByID` on a cache hit makes zero requests to Postgres — verified by a test with a mock repository counting calls
- [ ] `GetByID` on a cache miss reads from Postgres and warms the cache before returning the response to the client
- [ ] N concurrent `GetByID` calls with the same `id` on a cold cache → **exactly one** request to Postgres (test on `singleflight`, `N >= 50` goroutines)
- [ ] `Update`/`Delete` invalidate the cache **after** a successful commit to Postgres, not before
- [ ] A dropped Redis connection does not break `GetByID` — the request falls through to Postgres, response status does not change (test with an unreachable Redis address)
- [ ] Circuit breaker on Redis operations: after N consecutive failures it trips to `Open`, subsequent calls **do not wait for the Redis timeout**, they go straight to Postgres
- [ ] Cache TTL has ±10% jitter — no two consecutively written keys share the exact same TTL (test on value spread)
- [ ] `cache_hits_total`, `cache_misses_total`, `cache_errors_total`, `cache_breaker_state` metrics are exported on `/metrics`
- [ ] `go vet ./...` and `go test -race ./...` pass cleanly
- [ ] Zero goroutine leaks — the background cache write is bounded by its own timeout, not tied to the cancellable `request context`

---

## Technical requirements

### Mandatory

| Requirement | Details |
|---|---|
| `go-redis/v9` | client with explicit `DialTimeout`/`ReadTimeout`/`WriteTimeout`, pool via `PoolSize`/`MinIdleConns` |
| `golang.org/x/sync/singleflight` | collapses concurrent misses on the same key into a single trip to Postgres |
| Circuit breaker (`sony/gobreaker` or hand-rolled) | wraps **all** Redis operations; threshold and cooldown come from config, not hardcoded |
| Separate `context.WithTimeout` | the Redis timeout (ms) is strictly smaller than, and independent of, the Postgres query timeout |
| Jittered TTL | `ttl ± 10%`, otherwise a batch of keys warmed together expires together and slams the DB at once |
| Fail-open | a Redis error/timeout is not a request error, only a log line + metric, degrade to Postgres |
| Prometheus metrics | `cache_hits_total`, `cache_misses_total`, `cache_errors_total`, `cache_breaker_state` (0/1/2 = closed/half-open/open) |
| `LIMIT`/`OFFSET` in `List()` | without this — a sequential scan as the table grows, and no sane way to cache the list |

### Forbidden

- Caching `List()` without bounding the result set
- A blocking write to Redis **on the response path** to the client — `Set` only in the background with its own bounded timeout
- Redis as source of truth — TTL is mandatory and finite, `0`/unbounded keys are forbidden
- Silently swallowing Redis errors without a log line and without incrementing `cache_errors_total`
- Hardcoding the Redis address, pool size, or circuit breaker threshold in code — everything through `config.Load()`
- Invalidating the cache **before** the Postgres commit (operation order is fixed: DB first, then `DEL`)

---

## Concepts exercised

- **Cache-aside pattern** — the application itself decides when to read/write the cache; unlike write-through, the DB has no knowledge that Redis exists. Simpler to implement, but the desync window between DB and cache is your responsibility.

- **Cache stampede (thundering herd)** — when a hot key expires, hundreds of concurrent requests miss at once and hammer the DB simultaneously. `singleflight` collapses them into a single trip; the rest wait on the first result.

- **Circuit breaker (Closed / Open / Half-Open)** — when an external dependency degrades, it matters not to wait out a timeout on every request but to fail fast after a threshold of errors (`Open`), periodically probing for recovery (`Half-Open`). Without this, a stalled Redis adds a fixed delay to **every** request instead of failing all at once.

- **TTL jitter** — if 10,000 keys are warmed within the same second with an identical TTL, they all expire in the same second. Jitter spreads invalidation out over time.

- **Fail-open vs fail-closed** — for a read cache, fail-open degradation (fall through to the DB) is correct. For payments or rate limits, fail-closed would be needed (a refusal is safer than a wrong answer). Be ready to explain why fail-open is the right choice here.

- **Order: write → invalidate** — if the cache is invalidated before the DB commit and the process crashes in between, the old value gets read back into the cache before the transaction has even finished. The order is fixed, do not swap it.

---

## File structure

```
pg-crud/
├── main.go
├── config/
│   └── config.go              # + RedisAddr, RedisPoolSize, CacheTTL, BreakerThreshold
├── handler/
│   ├── user.go                # unchanged
│   └── user_test.go
├── repository/
│   ├── user.go                 # unchanged, List() migrated to LIMIT/OFFSET
│   ├── redis_client.go         # NEW: *redis.Client constructor
│   ├── user_cache.go           # NEW: cachedUserRepository — UserRepository decorator
│   └── user_cache_test.go      # NEW: singleflight, fail-open, breaker, jitter
├── metrics/
│   └── cache.go                # NEW: Prometheus collectors
├── migrations/
│   ├── 000001_create_users.up.sql
│   └── 000001_create_users.down.sql
├── docker-compose.yml          # + redis service
├── go.mod
├── README.md
└── task.md
```

---

## Breakdown by file

### `repository/user.go` (changes)

**Responsibility:** persistence layer, as before, plus pagination.

**Changes:**
- `List(ctx context.Context, limit, offset int) ([]*User, error)` — signature change, `ORDER BY id LIMIT $1 OFFSET $2`
- The `UserRepository.List` interface is updated in lockstep; `handler/user.go` parses `?limit=&offset=` from query parameters with defaults and an upper ceiling (`limit > 100` → `400`)

**Relations:** `cachedUserRepository.List` proxies straight through, no cache

---

### `repository/redis_client.go`

**Responsibility:** client constructor with explicit timeouts, so a stalled Redis never hangs handler goroutines.

**Functions:**
- `func NewRedisClient(ctx context.Context, cfg RedisConfig) (*redis.Client, error)` — `redis.NewClient` with `DialTimeout=2s`, `ReadTimeout=500ms`, `WriteTimeout=500ms`, `PoolSize`/`MinIdleConns` from config, `Ping` at startup with its own timeout

**Relations:** `main.go → repository.NewRedisClient(ctx, cfg)`

---

### `repository/user_cache.go`

**Responsibility:** cache-aside decorator over `UserRepository`, the only place aware of Redis.

**Types:**
- `type cachedUserRepository struct { next UserRepository; redis *redis.Client; breaker *gobreaker.CircuitBreaker; ttl time.Duration; sf singleflight.Group; metrics *metrics.CacheMetrics }`

**Functions:**
- `func NewCachedUserRepository(next UserRepository, rdb *redis.Client, ttl time.Duration, m *metrics.CacheMetrics) UserRepository`
- `func (r *cachedUserRepository) GetByID(ctx context.Context, id int64) (*User, error)` — Redis GET through the breaker → hit/miss/error metrics → `sf.Do` on miss → background `Set` with jitter
- `func (r *cachedUserRepository) Create/Update/Delete(...)` — proxy to `next`; `Update`/`Delete` call `invalidate` after `next` confirms success
- `func (r *cachedUserRepository) invalidate(ctx context.Context, id int64)` — `DEL` through the breaker, an error is only logged
- `func (r *cachedUserRepository) jitteredTTL() time.Duration` — `ttl ± 10%` via `math/rand`

**Relations:** `main.go` wraps `pgUserRepository` with this decorator before passing it to `handler.NewUserHandler`

---

### `metrics/cache.go`

**Responsibility:** Prometheus collectors for the cache layer, registered once at startup.

**Types:**
- `type CacheMetrics struct { Hits, Misses, Errors prometheus.Counter; BreakerState prometheus.Gauge }`

**Functions:**
- `func NewCacheMetrics(reg prometheus.Registerer) *CacheMetrics` — registers all collectors, panics on duplicate registration (fail-fast at startup, not at runtime)

**Relations:** passed into `repository.NewCachedUserRepository`, updated on every hit/miss/error and on breaker state change

---

### `main.go` (changes)

**Functions:**
- `func main()` — after `pgxpool.NewWithConfig`: `repository.NewRedisClient` → `metrics.NewCacheMetrics` → `gobreaker.NewCircuitBreaker` with the configured threshold → `repository.NewCachedUserRepository(pgRepo, redisClient, cfg.CacheTTL, cacheMetrics)` → `handler.NewUserHandler(cachedRepo)` → add `mux.Handle("/metrics", promhttp.Handler())`

**Relations:** the assembly point for every dependency, initialization order is fixed — Redis comes up after Postgres, before the handlers are created

---

## Architecture hints

**Circuit breaker around the Redis call:**
```go
func (r *cachedUserRepository) getFromCache(ctx context.Context, key string) ([]byte, error) {
    v, err := r.breaker.Execute(func() (interface{}, error) {
        cacheCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
        defer cancel()
        return r.redis.Get(cacheCtx, key).Bytes()
    })
    if err != nil {
        return nil, err // both redis.Nil and gobreaker.ErrOpenState land here — distinguish in the caller
    }
    return v.([]byte), nil
}
```
`gobreaker.ErrOpenState` is a separate branch from `redis.Nil`: **Open** means "didn't even try to reach it" — that is not a cache miss, it is a refusal to go to Redis at all.

**Invalidation strictly after commit:**
```go
func (r *cachedUserRepository) Update(ctx context.Context, id int64, name, email string) (*User, error) {
    u, err := r.next.Update(ctx, id, name, email) // source of truth first
    if err != nil {
        return nil, err
    }
    r.invalidate(ctx, id) // then the cache; if invalidate fails it's just logged, TTL cleans up
    return u, nil
}
```

**Background write without a goroutine leak:**
```go
func (r *cachedUserRepository) setAsync(u *User) {
    go func() {
        ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
        defer cancel()
        // ctx is not tied to the cancellable request context — otherwise
        // client-side cancellation would cut the cache warm-up short
        payload, _ := json.Marshal(u)
        _, _ = r.breaker.Execute(func() (interface{}, error) {
            return nil, r.redis.Set(ctx, userCacheKey(u.ID), payload, r.jitteredTTL()).Err()
        })
    }()
}
```
The goroutine cannot leak: it has its own time-bounded context (300 ms max), so it can never hang forever.

---

## Definition of Done

1. All acceptance criteria are covered by tests, `go test -race ./...` is green
2. A load run (`hey`/`wrk`, N=1000 concurrent requests against the same `id` with a cold cache) shows **one** request to Postgres in the logs — proves `singleflight` works
3. Deliberately kill Redis (`docker stop redis`) during load — the service keeps returning `200`, `cache_errors_total` climbs, the breaker trips to `Open`, response latency does not degrade to the Redis timeout on every request
4. Able to explain: why invalidation happens after the commit, not before; how `singleflight` differs from a plain per-key mutex; why the breaker's `Open` state is not the same as `redis.Nil`; why jitter on TTL when TTL could just be shortened instead

---

## Next step after this ships

Task #10 — Rate limiting at the `handler` level (token bucket over `net/http`, keyed by IP or API key). The cache protects against repeated reads of existing `id`s, but not against a flood of requests for **nonexistent** `id`s — those always miss the cache and hit Postgres directly; the cache is powerless against that by design.
