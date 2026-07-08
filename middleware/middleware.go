// Package middleware instruments the HTTP layer: per-request trace IDs,
// structured access logging and RED metrics.
package middleware

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"pg-crud/logging"
	"pg-crud/metrics"
)

var tracer = otel.Tracer("pg-crud/http")

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

// Instrument wraps mux with a root span, structured access logging and RED
// metrics. The metrics label is the matched route pattern looked up via
// mux.Handler — the raw URL path would make label cardinality unbounded
// (every distinct id becomes a new time series). The logger's trace_id is
// the OTel trace ID itself, not an independently generated value, so logs
// and traces for the same request are the same identifier, not two.
func Instrument(mux *http.ServeMux, m *metrics.HTTPMetrics) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		if pattern == "" {
			pattern = "unmatched"
		}

		ctx, span := tracer.Start(r.Context(), pattern, trace.WithAttributes(
			attribute.String("http.method", r.Method),
			attribute.String("http.route", pattern),
		))
		defer span.End()

		traceID := span.SpanContext().TraceID().String()
		logger := slog.Default().With("trace_id", traceID)
		ctx = logging.WithLogger(ctx, logger)
		w.Header().Set("X-Trace-Id", traceID)

		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		mux.ServeHTTP(rec, r.WithContext(ctx))
		elapsed := time.Since(start)

		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		span.SetAttributes(attribute.Int("http.status_code", status))
		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, "server error")
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
