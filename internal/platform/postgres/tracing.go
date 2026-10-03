package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("github.com/fiston-user/payments/internal/platform/postgres")

// queryTracer records a span for each query that runs inside a trace. Work
// that isn't part of one, like a relay polling for events, records nothing.
type queryTracer struct{}

func (queryTracer) TraceQueryStart(
	ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData,
) context.Context {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}
	// The SQL, but never the arguments: they're amounts, emails and IDs.
	ctx, _ = tracer.Start(ctx, spanName(d.SQL), trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(semconv.DBSystemNamePostgreSQL, semconv.DBQueryText(d.SQL)))
	return ctx
}

func (queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	span := trace.SpanFromContext(ctx)
	if d.Err != nil && !errors.Is(d.Err, pgx.ErrNoRows) {
		span.SetStatus(codes.Error, d.Err.Error())
	}
	span.End()
}

// spanName is the query's name, from sqlc's "-- name: GetBalance :one"
// comment, or else its first word, like BEGIN or SELECT.
func spanName(sql string) string {
	if name, ok := strings.CutPrefix(sql, "-- name: "); ok {
		name, _, _ = strings.Cut(name, " ")
		return name
	}
	word, _, _ := strings.Cut(strings.TrimSpace(sql), " ")
	return strings.ToUpper(word)
}
