// Package audit keeps a record of sensitive actions, in an audit_log table in
// the service's own database: who did what, to what, when, and from where.
// Services may add rows, and nothing else: their role can only INSERT.
package audit

import (
	"context"
	"fmt"

	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/fiston-user/payments/internal/platform/mtls"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/trace"
)

// An Event is one sensitive action.
type Event struct {
	Actor   string // who: a user's ID
	Action  string // what, like transfer.approve
	Target  string // to what: a transfer's ID, say
	Details any    // anything else worth keeping, as JSON
}

// DB is a pool or a transaction: anything that runs a statement.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Record adds e to the audit log in db: the transaction that does the action,
// so the action and its record commit together, or not at all. Where it came
// from is read from ctx: the service that called us, by its certificate, the
// request ID, and the trace.
func Record(ctx context.Context, db DB, e Event) error {
	var traceID string
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		traceID = sc.TraceID().String()
	}
	_, err := db.Exec(ctx, `INSERT INTO audit_log
		(actor, action, target, details, client, request_id, trace_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.Actor, e.Action, e.Target, e.Details, mtls.PeerFrom(ctx), httpx.RequestIDFrom(ctx), traceID)
	if err != nil {
		return fmt.Errorf("audit %s: %w", e.Action, err)
	}
	return nil
}
