package notification

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/fiston-user/payments/internal/notification/notificationdb"
	"github.com/fiston-user/payments/internal/platform/logging"
	"github.com/nats-io/nats.go/jetstream"
)

// ackWait is how long the server waits for our answer before it delivers a
// message again. Handling one gets half of it.
const ackWait = 30 * time.Second

// Consume creates our durable pull consumer on stream, or updates it, and
// handles its messages until the returned ConsumeContext stops.
func (n *Notifier) Consume(
	ctx context.Context, js jetstream.JetStream, stream string, subjects ...string,
) (jetstream.ConsumeContext, error) {
	consumer, err := js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable:        "notification",
		Description:    "tells users what happened to their money",
		FilterSubjects: subjects,
		DeliverPolicy:  jetstream.DeliverNewPolicy, // no emails about last month
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        ackWait,
		MaxDeliver:     n.MaxAttempts,
		MaxAckPending:  100, // in flight at once, across every instance
	})
	if err != nil {
		return nil, fmt.Errorf("consumer on %s: %w", stream, err)
	}
	n.logger.Info("consuming", "stream", stream, "subjects", subjects)
	return consumer.Consume(n.handle)
}

// handle processes one message, and tells the server how it went: done, try
// again later, or never again.
func (n *Notifier) handle(msg jetstream.Msg) {
	meta, err := msg.Metadata()
	if err != nil {
		n.logger.Error("not a jetstream message", "subject", msg.Subject(), "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ackWait/2)
	defer cancel()
	ctx = logging.With(ctx, slog.String("stream", meta.Stream),
		slog.Uint64("seq", meta.Sequence.Stream), slog.Uint64("delivery", meta.NumDelivered))

	err = n.process(ctx, msg.Data())
	var permanent *PermanentError
	switch {
	case err == nil:
		msg.Ack() // if the ack gets lost, we see it again, and skip it
	case !errors.As(err, &permanent) && meta.NumDelivered < uint64(n.MaxAttempts):
		delay := n.backoff(meta.NumDelivered)
		n.logger.WarnContext(ctx, "notification failed, retrying", "in", delay, "err", err)
		msg.NakWithDelay(delay)
	default:
		n.logger.ErrorContext(ctx, "notification failed for good: dead letter", "err", err)
		n.deadLetter(ctx, msg, err)
	}
}

// deadLetter keeps a message we gave up on, and tells the server to stop
// delivering it.
func (n *Notifier) deadLetter(ctx context.Context, msg jetstream.Msg, cause error) {
	meta, _ := msg.Metadata() // handle checked it
	err := n.store.DeadLetter(ctx, notificationdb.InsertDeadLetterParams{
		Stream:     meta.Stream,
		Seq:        int64(meta.Sequence.Stream),
		Subject:    msg.Subject(),
		Payload:    msg.Data(),
		Error:      cause.Error(),
		Deliveries: int32(meta.NumDelivered),
	})
	if err != nil {
		// Not kept: let the server deliver it again, if it still will.
		n.logger.ErrorContext(ctx, "keep dead letter", "err", err)
		msg.Nak()
		return
	}
	msg.Term()
}

// backoff doubles with each delivery, up to a minute, with jitter.
func (n *Notifier) backoff(delivery uint64) time.Duration {
	d := min(n.Backoff<<min(delivery-1, 16), time.Minute)
	return (d/2 + rand.N(d/2+1)).Round(time.Millisecond)
}

// Replay sends the dead letters again, through the same code, and marks the
// ones that work. Notify is idempotent, so replaying never sends twice.
func (n *Notifier) Replay(ctx context.Context) (int, error) {
	letters, err := n.store.DeadLetters(ctx)
	if err != nil {
		return 0, fmt.Errorf("dead letters: %w", err)
	}
	replayed := 0
	for _, d := range letters {
		ctx := logging.With(ctx, slog.String("stream", d.Stream), slog.Int64("seq", d.Seq))
		if err := n.process(ctx, d.Payload); err != nil {
			n.logger.WarnContext(ctx, "replay failed, keeping it", "err", err)
			continue
		}
		if err := n.store.Replayed(ctx, d); err != nil {
			return replayed, err
		}
		replayed++
	}
	return replayed, nil
}
