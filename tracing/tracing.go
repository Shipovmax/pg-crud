// Package tracing wires up the OpenTelemetry TracerProvider used across
// the service. The HTTP layer, repository and cache decorator all pull
// their tracer via otel.Tracer(name) — the global registered here — so no
// tracer needs to be threaded through constructors.
package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// NewTracerProvider builds and registers a TracerProvider as the global
// default. With endpoint empty it exports to stdout — spans are visible
// locally without standing up a collector; this is a deliberate dev-mode
// fallback, not a silent no-op, and is unsuitable for production traffic
// volume since every span is written to stdout synchronously in batches.
func NewTracerProvider(ctx context.Context, endpoint, serviceName string) (*sdktrace.TracerProvider, error) {
	exporter, err := newExporter(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("otel exporter: %w", err)
	}

	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(serviceName)))
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return tp, nil
}

func newExporter(ctx context.Context, endpoint string) (sdktrace.SpanExporter, error) {
	if endpoint == "" {
		return stdouttrace.New(stdouttrace.WithPrettyPrint())
	}
	return otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
}
