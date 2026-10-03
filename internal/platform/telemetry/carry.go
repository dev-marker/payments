package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// Carry returns ctx's trace context as a map, to keep with work that goes on
// later, like a row in the outbox. Extract continues the trace from it.
func Carry(ctx context.Context) propagation.MapCarrier {
	c := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, c)
	return c
}

// Inject writes ctx's trace context into c, like a message's headers.
func Inject(ctx context.Context, c propagation.TextMapCarrier) {
	otel.GetTextMapPropagator().Inject(ctx, c)
}

// Extract returns ctx, with the trace context in c: the next span we start
// is a child of the span that wrote it.
func Extract(ctx context.Context, c propagation.TextMapCarrier) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, c)
}
