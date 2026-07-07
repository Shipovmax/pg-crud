// Package logging carries a request-scoped structured logger through
// context, so every layer logs with the same trace_id without knowing
// where it came from.
package logging

import (
	"context"
	"log/slog"
)

type ctxKey struct{}

// WithLogger returns a child context carrying l.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext returns the logger stored by WithLogger, or slog.Default()
// when the context carries none (background jobs, tests).
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
