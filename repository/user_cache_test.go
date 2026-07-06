package repository

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker"

	"pg-crud/metrics"
)

// countingUserRepository is a fake UserRepository that counts GetByID calls
// so tests can assert Postgres is (or isn't) reached.
type countingUserRepository struct {
	mu        sync.Mutex
	calls     int32
	delay     time.Duration
	getErr    error
	updateErr error
	user      *User
}

func (f *countingUserRepository) Create(_ context.Context, _, _ string) (*User, error) {
	return nil, errors.New("not implemented")
}

func (f *countingUserRepository) GetByID(_ context.Context, _ int64) (*User, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.getErr != nil {
		return nil, f.getErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.user == nil {
		return nil, ErrNotFound
	}
	u := *f.user
	return &u, nil
}

func (f *countingUserRepository) List(_ context.Context, _, _ int) ([]*User, error) {
	return nil, errors.New("not implemented")
}

func (f *countingUserRepository) Update(_ context.Context, _ int64, name, email string) (*User, error) {
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.user.Name = name
	f.user.Email = email
	u := *f.user
	return &u, nil
}

func (f *countingUserRepository) Delete(_ context.Context, _ int64) error {
	return nil
}

func (f *countingUserRepository) callCount() int {
	return int(atomic.LoadInt32(&f.calls))
}

// newTestCache builds a cachedUserRepository backed by a real (in-memory)
// Redis server, so tests exercise the actual GET/SET/DEL/TTL wire protocol
// instead of a hand-rolled mock.
func newTestCache(t *testing.T, next UserRepository, ttl time.Duration) (*cachedUserRepository, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })

	m := metrics.NewCacheMetrics(prometheus.NewRegistry())
	repo := NewCachedUserRepository(next, rdb, ttl, BreakerConfig{Threshold: 3, Cooldown: 50 * time.Millisecond}, m)
	return repo.(*cachedUserRepository), mr
}

// waitForCachedKey polls until setAsync's background write for id lands,
// since cache warming is fire-and-forget on the response path.
func waitForCachedKey(t *testing.T, r *cachedUserRepository, id int64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := r.redis.Get(context.Background(), userCacheKey(id)).Err(); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("cache key for user %d was not warmed in time", id)
}

func TestGetByID_CacheHitSkipsPostgres(t *testing.T) {
	next := &countingUserRepository{user: &User{ID: 1, Name: "Alice", Email: "alice@example.com"}}
	repo, _ := newTestCache(t, next, time.Minute)
	ctx := context.Background()

	if _, err := repo.GetByID(ctx, 1); err != nil {
		t.Fatalf("first GetByID: %v", err)
	}
	if got := next.callCount(); got != 1 {
		t.Fatalf("Postgres calls after cold GetByID = %d, want 1", got)
	}

	waitForCachedKey(t, repo, 1)

	if _, err := repo.GetByID(ctx, 1); err != nil {
		t.Fatalf("second GetByID: %v", err)
	}
	if got := next.callCount(); got != 1 {
		t.Fatalf("Postgres calls after warm GetByID = %d, want 1 (cache hit must not reach Postgres)", got)
	}
}

func TestGetByID_SingleflightCollapsesConcurrentMisses(t *testing.T) {
	next := &countingUserRepository{
		user:  &User{ID: 7, Name: "Bob", Email: "bob@example.com"},
		delay: 100 * time.Millisecond, // keeps the in-flight call open so concurrent callers pile up
	}
	repo, _ := newTestCache(t, next, time.Minute)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make([]error, n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			_, err := repo.GetByID(context.Background(), 7)
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	if got := next.callCount(); got != 1 {
		t.Fatalf("Postgres calls = %d, want exactly 1 (singleflight must collapse concurrent misses)", got)
	}

	// Drain the background cache warm-up before the test's t.Cleanup
	// closes the Redis client out from under it.
	waitForCachedKey(t, repo, 7)
}

func TestGetByID_FailsOpenWhenRedisUnreachable(t *testing.T) {
	next := &countingUserRepository{user: &User{ID: 3, Name: "Eve", Email: "eve@example.com"}}

	// 127.0.0.1:1 refuses the connection immediately instead of hanging,
	// simulating "Redis is down" without waiting out a dial timeout.
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond})
	t.Cleanup(func() { rdb.Close() })

	m := metrics.NewCacheMetrics(prometheus.NewRegistry())
	repo := NewCachedUserRepository(next, rdb, time.Minute, BreakerConfig{Threshold: 100, Cooldown: time.Second}, m).(*cachedUserRepository)

	u, err := repo.GetByID(context.Background(), 3)
	if err != nil {
		t.Fatalf("GetByID with unreachable Redis returned error, want fail-open degradation: %v", err)
	}
	if u.ID != 3 {
		t.Fatalf("got user %+v, want id 3", u)
	}
	if got := next.callCount(); got != 1 {
		t.Fatalf("Postgres calls = %d, want 1", got)
	}
}

func TestCircuitBreaker_OpensAfterThresholdAndSkipsRedis(t *testing.T) {
	next := &countingUserRepository{user: &User{ID: 5, Name: "Sam", Email: "sam@example.com"}}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond})
	t.Cleanup(func() { rdb.Close() })

	reg := prometheus.NewRegistry()
	m := metrics.NewCacheMetrics(reg)
	const threshold = 3
	repo := NewCachedUserRepository(next, rdb, time.Minute, BreakerConfig{Threshold: threshold, Cooldown: time.Minute}, m).(*cachedUserRepository)

	for i := range threshold {
		if _, err := repo.GetByID(context.Background(), 5); err != nil {
			t.Fatalf("GetByID call %d: %v", i, err)
		}
	}

	if state := repo.breaker.State(); state != gobreaker.StateOpen {
		t.Fatalf("breaker state = %v, want Open after %d consecutive Redis failures", state, threshold)
	}
	if got := testutil.ToFloat64(m.BreakerState); got != float64(gobreaker.StateOpen) {
		t.Fatalf("cache_breaker_state = %v, want %v", got, float64(gobreaker.StateOpen))
	}

	// With the breaker Open, further calls must fail fast (ErrOpenState)
	// instead of attempting to dial Redis and waiting out DialTimeout.
	start := time.Now()
	if _, err := repo.GetByID(context.Background(), 5); err != nil {
		t.Fatalf("GetByID with open breaker: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Millisecond {
		t.Fatalf("GetByID took %v with breaker Open, want near-instant (no Redis attempt)", elapsed)
	}
}

func TestJitteredTTL_SpreadsWithinTenPercent(t *testing.T) {
	repo := &cachedUserRepository{ttl: 100 * time.Second}

	min := time.Duration(float64(repo.ttl) * 0.9)
	max := time.Duration(float64(repo.ttl) * 1.1)

	seen := make(map[time.Duration]bool)
	for range 20 {
		d := repo.jitteredTTL()
		if d < min || d > max {
			t.Fatalf("jittered TTL %v out of [%v, %v] range", d, min, max)
		}
		seen[d] = true
	}
	if len(seen) < 2 {
		t.Fatalf("20 jittered TTLs produced only %d distinct value(s), want spread", len(seen))
	}
}

func TestUpdate_InvalidatesCacheOnlyAfterCommit(t *testing.T) {
	next := &countingUserRepository{user: &User{ID: 9, Name: "Ann", Email: "ann@example.com"}}
	repo, mr := newTestCache(t, next, time.Minute)
	ctx := context.Background()

	if _, err := repo.GetByID(ctx, 9); err != nil {
		t.Fatalf("warm cache: %v", err)
	}
	waitForCachedKey(t, repo, 9)

	next.updateErr = errors.New("db down")
	if _, err := repo.Update(ctx, 9, "Ann2", "ann2@example.com"); err == nil {
		t.Fatal("expected Update to fail")
	}
	if !mr.Exists(userCacheKey(9)) {
		t.Fatal("cache key removed despite a failed commit; invalidation must happen strictly after the commit")
	}

	next.updateErr = nil
	if _, err := repo.Update(ctx, 9, "Ann3", "ann3@example.com"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if mr.Exists(userCacheKey(9)) {
		t.Fatal("cache key still present after a successful Update; invalidate should have deleted it")
	}
}
