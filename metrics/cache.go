package metrics

import "github.com/prometheus/client_golang/prometheus"

// CacheMetrics groups the Prometheus collectors for the cache-aside layer.
type CacheMetrics struct {
	Hits         prometheus.Counter
	Misses       prometheus.Counter
	Errors       prometheus.Counter
	BreakerState prometheus.Gauge
}

// NewCacheMetrics registers the cache collectors against reg. It panics on
// duplicate registration, so a misconfigured wiring fails fast at startup
// instead of silently at the first cache access.
func NewCacheMetrics(reg prometheus.Registerer) *CacheMetrics {
	m := &CacheMetrics{
		Hits: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cache_hits_total",
			Help: "Number of cache hits for GetByID lookups.",
		}),
		Misses: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cache_misses_total",
			Help: "Number of cache misses for GetByID lookups.",
		}),
		Errors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cache_errors_total",
			Help: "Number of Redis operation failures (timeouts, breaker open, transport errors).",
		}),
		BreakerState: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "cache_breaker_state",
			Help: "Circuit breaker state: 0=closed, 1=half-open, 2=open.",
		}),
	}
	reg.MustRegister(m.Hits, m.Misses, m.Errors, m.BreakerState)
	return m
}
