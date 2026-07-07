package metrics

import "github.com/prometheus/client_golang/prometheus"

// HTTPMetrics groups the RED collectors for the HTTP layer. Labels use the
// registered route pattern, never the raw URL path, to keep label
// cardinality bounded regardless of what clients request.
type HTTPMetrics struct {
	Requests *prometheus.CounterVec   // method, pattern, status
	Duration *prometheus.HistogramVec // method, pattern
}

// NewHTTPMetrics registers the HTTP collectors against reg, panicking on
// duplicate registration (fail-fast at startup).
func NewHTTPMetrics(reg prometheus.Registerer) *HTTPMetrics {
	m := &HTTPMetrics{
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Number of HTTP requests by method, route pattern and status code.",
		}, []string{"method", "pattern", "status"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency by method and route pattern.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "pattern"}),
	}
	reg.MustRegister(m.Requests, m.Duration)
	return m
}
