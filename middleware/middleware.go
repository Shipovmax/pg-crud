// Package middleware instruments the HTTP layer: per-request trace IDs,
// structured access logging and RED metrics.
package middleware

import (
	"crypto/rand"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"pg-crud/logging"
	"pg-crud/metrics"
)

// statusRecorder captures the status code written by the handler; the
// zero value means the handler never called WriteHeader explicitly.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Instrument wraps mux with trace-id injection, structured access logging
// and RED metrics. The metrics label is the matched route pattern looked
// up via mux.Handler — the raw URL path would make label cardinality
// unbounded (every distinct id becomes a new time series).
func Instrument(mux *http.ServeMux, m *metrics.HTTPMetrics) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := rand.Text()
		logger := slog.Default().With("trace_id", traceID)
		ctx := logging.WithLogger(r.Context(), logger)
		w.Header().Set("X-Trace-Id", traceID)

		_, pattern := mux.Handler(r)
		if pattern == "" {
			pattern = "unmatched"
		}

		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		mux.ServeHTTP(rec, r.WithContext(ctx))
		elapsed := time.Since(start)

		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		m.Requests.WithLabelValues(r.Method, pattern, strconv.Itoa(status)).Inc()
		m.Duration.WithLabelValues(r.Method, pattern).Observe(elapsed.Seconds())

		logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"pattern", pattern,
			"status", status,
			"duration_ms", elapsed.Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}
