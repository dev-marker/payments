// Package outbox publishes the events a service writes to its outbox table,
// in the same database transaction as the change each event describes.
package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/fiston-user/payments/internal/platform/natsx"
	"github.com/fiston-user/payments/internal/platform/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("github.com/fiston-user/payments/internal/platform/outbox")

// A Relay publishes the events in the outbox table to JetStream, oldest
// first, and marks them published. That's two writes again, so a crash in
// between publishes an event twice: delivery is at least once. JetStream
// drops the second copy, because both carry the event's ID as Nats-Msg-Id.
type Relay struct {
	pool      *pgxpool.Pool
	js        jetstream.JetStream
	logger    *slog.Logger
	BatchSize int           // events per database transaction
	MaxWait   time.Duration // the longest we wait before looking again
}

// NewRelay returns a Relay for the outbox table in pool's database.
func NewRelay(pool *pgxpool.Pool, js jetstream.JetStream, logger *slog.Logger) *Relay {
	return &Relay{pool: pool, js: js, logger: logger, BatchSize: 100, MaxWait: 5 * time.Second}
}

// message is an outbox row, waiting to be published.
type message struct {
	Seq          int64
	EventID      uuid.UUID
	Subject      string
	Payload      []byte
	TraceContext propagation.MapCarrier
}

// PublishBatch publishes up to BatchSize events, and returns how many.
func (r *Relay) PublishBatch(ctx context.Context) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("outbox: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	// FOR UPDATE locks the rows until we commit. SKIP LOCKED passes over rows
	// another relay has locked, so several instances share the work, and
	// never publish the same rows at the same time.
	rows, _ := tx.Query(ctx, `
		SELECT seq, event_id, subject, payload, trace_context FROM outbox
		WHERE published_at IS NULL
		ORDER BY seq
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, r.BatchSize)
	batch, err := pgx.CollectRows(rows, pgx.RowToStructByPos[message])
	if err != nil {
		return 0, fmt.Errorf("outbox: read: %w", err)
	}

	var published []int64
	var pubErr error
	for _, m := range batch {
		if pubErr = r.publish(ctx, m); pubErr != nil {
			break // stop here, so no event overtakes this one
		}
		published = append(published, m.Seq)
	}
	if len(published) == 0 {
		return 0, pubErr
	}
	// Mark what made it, even when a later event failed.
	_, err = tx.Exec(ctx, `UPDATE outbox SET published_at = now() WHERE seq = ANY($1)`, published)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return 0, fmt.Errorf("outbox: mark published: %w", err)
	}
	return len(published), pubErr
}

// publish sends m to JetStream, in a span that continues the trace m was
// written in, and passes that trace on in the message's headers.
func (r *Relay) publish(ctx context.Context, m message) error {
	ctx, span := tracer.Start(telemetry.Extract(ctx, m.TraceContext), "publish "+m.Subject,
		trace.WithSpanKind(trace.SpanKindProducer))
	defer span.End()
	msg := &nats.Msg{Subject: m.Subject, Data: m.Payload, Header: nats.Header{}}
	telemetry.Inject(ctx, natsx.HeaderCarrier{Header: msg.Header})
	if _, err := r.js.PublishMsg(ctx, msg, jetstream.WithMsgID(m.EventID.String())); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("outbox: publish %s: %w", m.EventID, err)
	}
	return nil
}

// Run publishes events until ctx is canceled. After a full batch, it goes
// again right away. Otherwise it waits: until Postgres says there are new
// events, or for a while that doubles each time there's nothing to do.
func (r *Relay) Run(ctx context.Context) {
	r.logger.Info("outbox relay started", "batch_size", r.BatchSize)
	wake := make(chan struct{}, 1)
	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Go(func() { r.listen(ctx, wake) })

	const minWait = 50 * time.Millisecond
	wait := minWait
	for {
		n, err := r.PublishBatch(ctx)
		if err != nil && ctx.Err() == nil {
			r.logger.Warn("outbox relay: publish failed, will retry", "err", err)
		}
		if n == r.BatchSize {
			wait = minWait
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
			wait = minWait
		case <-time.After(wait):
			wait = min(wait*2, r.MaxWait)
		}
	}
}

// listen sends to wake whenever Postgres notifies the outbox channel, which
// a trigger does each time a transaction that wrote events commits. If the
// connection fails, Run still polls, and listen connects again.
func (r *Relay) listen(ctx context.Context, wake chan<- struct{}) {
	for ctx.Err() == nil {
		err := r.waitForNotifications(ctx, wake)
		if ctx.Err() != nil {
			return
		}
		r.logger.Warn("outbox relay: listen failed, polling only", "err", err)
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
}

func (r *Relay) waitForNotifications(ctx context.Context, wake chan<- struct{}) error {
	pc, err := r.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	// A listening connection can't go back to the pool: it's ours for good.
	conn := pc.Hijack()
	defer conn.Close(context.Background())

	if _, err := conn.Exec(ctx, "LISTEN outbox"); err != nil {
		return err
	}
	for {
		if _, err := conn.WaitForNotification(ctx); err != nil {
			return err
		}
		select {
		case wake <- struct{}{}:
		default: // a wake-up is already waiting
		}
	}
}
