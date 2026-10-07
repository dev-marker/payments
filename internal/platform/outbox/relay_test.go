package outbox_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/fiston-user/payments/internal/platform/natsx"
	"github.com/fiston-user/payments/internal/platform/outbox"
	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/fiston-user/payments/internal/platform/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/trace"
)

// newOutbox returns a pool of one connection, with an outbox of its own: a
// temporary table, like the wallet's, that only this connection sees, and
// that hides the real one. The relay sees our events, and no one else's.
func newOutbox(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	pool, err := postgres.NewPool(t.Context(), postgresURL, 1)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(t.Context(), `CREATE TEMPORARY TABLE outbox (LIKE public.outbox INCLUDING ALL)`)
	if err != nil {
		t.Fatalf("create outbox: %v", err)
	}
	return pool
}

// newStream creates a stream for this test alone, on subjects no one else uses.
func newStream(t *testing.T) (jetstream.JetStream, jetstream.Stream, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	name := "TEST_" + rand.Text()
	stream, err := js.CreateStream(t.Context(), jetstream.StreamConfig{
		Name:       name,
		Subjects:   []string{name + ".>"},
		Storage:    jetstream.MemoryStorage,
		Duplicates: time.Minute,
	})
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	t.Cleanup(func() { js.DeleteStream(context.Background(), name) })
	return js, stream, name
}

func TestRelay(t *testing.T) {
	t.Parallel()
	pool := newOutbox(t)
	js, stream, prefix := newStream(t)

	// Three events, written the way a service writes them.
	var ids []uuid.UUID
	for i := range 3 {
		id := uuid.New()
		ids = append(ids, id)
		subject, payload := fmt.Sprintf("%s.test.%d", prefix, i), fmt.Sprintf(`{"n": %d}`, i)
		_, err := pool.Exec(t.Context(),
			`INSERT INTO outbox (event_id, subject, payload) VALUES ($1, $2, $3)`, id, subject, payload)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	relay := outbox.NewRelay(pool, js, slog.New(slog.DiscardHandler))
	relay.BatchSize = 2
	publish := func(want int) {
		t.Helper()
		n, err := relay.PublishBatch(t.Context())
		if err != nil || n != want {
			t.Fatalf("PublishBatch = %d, %v; want %d", n, err, want)
		}
	}
	// Two batches publish all three, in order, and a third finds nothing.
	publish(2)
	publish(1)
	publish(0)
	expectStream(t, stream, ids)

	// A crash after publishing, before marking: every event looks new again.
	if _, err := pool.Exec(t.Context(), `UPDATE outbox SET published_at = NULL`); err != nil {
		t.Fatalf("unmark: %v", err)
	}
	publish(2)
	publish(1)
	// JetStream recognised every Nats-Msg-Id: still three messages, not six.
	expectStream(t, stream, ids)
}

// The trace an event was written in goes on in its message's headers, with
// tracing off, as in every test: the context travels all the same.
func TestRelayContinuesTrace(t *testing.T) {
	t.Parallel()
	pool := newOutbox(t)
	js, stream, prefix := newStream(t)
	if _, err := telemetry.Setup(t.Context(), telemetry.Options{}); err != nil {
		t.Fatalf("telemetry: %v", err)
	}
	// The span of the request that wrote the event.
	written := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x4b, 0xf9, 0x2f, 0x35},
		SpanID:     trace.SpanID{0x00, 0xf0, 0x67},
		TraceFlags: trace.FlagsSampled,
	})
	carrier := telemetry.Carry(trace.ContextWithSpanContext(t.Context(), written))
	_, err := pool.Exec(t.Context(), `INSERT INTO outbox (event_id, subject, payload, trace_context)
		VALUES ($1, $2, '{}', $3)`, uuid.New(), prefix+".test", carrier)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	relay := outbox.NewRelay(pool, js, slog.New(slog.DiscardHandler))
	if n, err := relay.PublishBatch(t.Context()); n != 1 || err != nil {
		t.Fatalf("PublishBatch = %d, %v; want 1, nil", n, err)
	}
	msg, err := stream.GetMsg(t.Context(), 1)
	if err != nil {
		t.Fatalf("get message: %v", err)
	}
	ctx := telemetry.Extract(t.Context(), natsx.HeaderCarrier{Header: msg.Header})
	if got := trace.SpanContextFromContext(ctx).TraceID(); got != written.TraceID() {
		t.Errorf("message's trace ID is %s, want %s", got, written.TraceID())
	}
}

// expectStream checks that the stream holds exactly the events ids, in order.
func expectStream(t *testing.T, stream jetstream.Stream, ids []uuid.UUID) {
	t.Helper()
	info, err := stream.Info(t.Context())
	if err != nil {
		t.Fatalf("stream info: %v", err)
	}
	if got := info.State.Msgs; got != uint64(len(ids)) {
		t.Fatalf("stream has %d messages, want %d", got, len(ids))
	}
	for i, id := range ids {
		msg, err := stream.GetMsg(t.Context(), uint64(i+1))
		if err != nil {
			t.Fatalf("get message %d: %v", i+1, err)
		}
		if got := msg.Header.Get(jetstream.MsgIDHeader); got != id.String() {
			t.Errorf("message %d has Nats-Msg-Id %s, want %s", i+1, got, id)
		}
	}
}
