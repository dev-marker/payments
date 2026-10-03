// Package telemetry sets up OpenTelemetry tracing, and carries trace context
// across the hops that aren't HTTP or gRPC calls.
package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

// Options says which service is being traced.
type Options struct {
	Service string
	Version string
	Env     string // development or production
}

// Setup sends traces to the collector in OTEL_EXPORTER_OTLP_ENDPOINT, the
// standard variable. Without one, tracing is off: spans cost next to nothing,
// and tests need no collector. Call shutdown before exiting, to send the
// spans still waiting in the batch.
func Setup(ctx context.Context, o Options) (shutdown func(context.Context) error, err error) {
	// Trace context travels in the W3C headers, traceparent and tracestate,
	// and so does baggage. With tracing off, we still pass them on.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("telemetry: %w", err)
	}
	// Errors the SDK can't return to anyone, like an export that failed.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Warn("telemetry", "err", err)
	}))

	// What every span says about where it came from. We list it ourselves,
	// instead of detecting it: only what we choose goes into our traces.
	res := resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName(o.Service),
		semconv.ServiceVersion(o.Version),
		semconv.DeploymentEnvironmentName(o.Env),
	)
	// Keep every trace in development, and one in ten in production. The
	// decision is made once, where a trace starts: everyone downstream
	// follows their parent, so a trace is kept whole, or not at all.
	ratio := 1.0
	if o.Env == "production" {
		ratio = 0.1
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter), // in the background, a batch at a time
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}
