package logging

import (
	"context"
	"log/slog"
	"slices"

	"go.opentelemetry.io/otel/trace"
)

type attrsKey struct{}

// With returns a copy of ctx that carries attrs. Loggers made by New add them
// to every line logged with that context, through methods like InfoContext.
func With(ctx context.Context, attrs ...slog.Attr) context.Context {
	prev, _ := ctx.Value(attrsKey{}).([]slog.Attr)
	return context.WithValue(ctx, attrsKey{}, slices.Concat(prev, attrs))
}

// contextHandler adds the attributes stored by With to each record, and then
// passes it on to the real handler.
type contextHandler struct {
	slog.Handler
}

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if attrs, ok := ctx.Value(attrsKey{}).([]slog.Attr); ok {
		r.AddAttrs(attrs...)
	}
	// The trace this line belongs to: search for its ID in Jaeger, or in the
	// logs of every service.
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs and WithGroup must wrap their result too. The embedded methods
// would return the bare inner handler, and logger.With would drop our wrapper.
func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}
