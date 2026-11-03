package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fiston-user/payments/internal/platform/events"
	"github.com/fiston-user/payments/internal/platform/natsx"
	"github.com/fiston-user/payments/internal/platform/telemetry"
	"github.com/fiston-user/payments/internal/transfer/transferevents"
	"github.com/fiston-user/payments/internal/webhook/webhookdb"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("github.com/fiston-user/payments/internal/webhook")

// A Payload is what a receiver gets: the event, in an envelope of our own,
// which stays the same whatever changes inside our system.
type Payload struct {
	ID        uuid.UUID       `json:"id"`
	Type      string          `json:"type"`
	CreatedAt time.Time       `json:"created_at"`
	Data      json.RawMessage `json:"data"`
}

var errUndecodable = errors.New("undecodable event")

// FanOut turns transfer events into deliveries, one for each endpoint that
// wants them. It's a durable consumer, like the notification service's.
type FanOut struct {
	q      *webhookdb.Queries
	wake   func()
	logger *slog.Logger
}

// NewFanOut returns a FanOut that wakes the worker when there's work.
func NewFanOut(q *webhookdb.Queries, wake func(), logger *slog.Logger) *FanOut {
	return &FanOut{q: q, wake: wake, logger: logger}
}

// Consume starts the durable consumer on the TRANSFER stream.
func (f *FanOut) Consume(ctx context.Context, js jetstream.JetStream) (jetstream.ConsumeContext, error) {
	if err := natsx.WaitForStream(ctx, js, transferevents.Stream); err != nil {
		return nil, err
	}
	consumer, err := js.CreateOrUpdateConsumer(ctx, transferevents.Stream, jetstream.ConsumerConfig{
		Durable:        "webhook",
		Description:    "turns transfer events into webhook deliveries",
		FilterSubjects: []string{"transfer.events.>"},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        30 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("consumer on %s: %w", transferevents.Stream, err)
	}
	return consumer.Consume(f.handle)
}

func (f *FanOut) handle(msg jetstream.Msg) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ctx, span := tracer.Start(telemetry.Extract(ctx, natsx.HeaderCarrier{Header: msg.Headers()}),
		"fan out "+msg.Subject(), trace.WithSpanKind(trace.SpanKindConsumer))
	defer span.End()

	n, err := f.fanOut(ctx, msg.Data())
	switch {
	case errors.Is(err, errUndecodable):
		f.logger.ErrorContext(ctx, "dropping event", "subject", msg.Subject(), "err", err)
		msg.Term() // retrying can't fix it
	case err != nil:
		f.logger.WarnContext(ctx, "fan out failed, retrying", "err", err)
		msg.NakWithDelay(5 * time.Second)
	default:
		if n > 0 {
			f.wake()
		}
		msg.Ack()
	}
}

// fanOut writes the deliveries for one event. The same event, delivered again
// by NATS, finds its deliveries already there, and writes nothing.
func (f *FanOut) fanOut(ctx context.Context, data []byte) (int64, error) {
	var evt events.Event
	var t transferevents.Transfer
	if err := json.Unmarshal(data, &evt); err != nil {
		return 0, fmt.Errorf("%w: %w", errUndecodable, err)
	}
	if err := json.Unmarshal(evt.Data, &t); err != nil {
		return 0, fmt.Errorf("%w: %s: %w", errUndecodable, evt.Type, err)
	}
	payload, err := json.Marshal(Payload{ID: evt.ID, Type: evt.Type, CreatedAt: evt.OccurredAt, Data: evt.Data})
	if err != nil {
		return 0, err
	}
	return f.q.FanOut(ctx, webhookdb.FanOutParams{
		UserID: t.UserID, EventID: evt.ID, EventType: evt.Type, Payload: payload,
	})
}
