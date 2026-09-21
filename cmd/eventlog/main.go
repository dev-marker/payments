// Command eventlog prints the wallet's events as they happen. It's a durable
// consumer: stop it, and it picks up where it left off when it's back.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fiston-user/payments/internal/platform/events"
	"github.com/fiston-user/payments/internal/platform/natsx"
	"github.com/fiston-user/payments/internal/wallet/walletevents"
	"github.com/nats-io/nats.go/jetstream"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("eventlog failed", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	nc, err := natsx.Connect(cmp.Or(os.Getenv("NATS_URL"), "nats://localhost:4222"), "eventlog", logger)
	if err != nil {
		return err
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}

	// A durable consumer lives on the server, under its name: it remembers
	// the last event we acknowledged, whether we're running or not.
	consumer, err := js.CreateOrUpdateConsumer(ctx, walletevents.Stream, jetstream.ConsumerConfig{
		Durable:       "eventlog",
		Description:   "prints wallet events, for development",
		FilterSubject: "wallet.events.>",
		DeliverPolicy: jetstream.DeliverAllPolicy, // a new consumer starts at the first event
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second, // not acknowledged by then: delivered again
		MaxDeliver:    5,
	})
	if err != nil {
		return fmt.Errorf("consumer: %w", err)
	}

	cc, err := consumer.Consume(func(msg jetstream.Msg) {
		var evt events.Event
		if err := json.Unmarshal(msg.Data(), &evt); err != nil {
			// It will never decode: don't send it again.
			logger.Error("bad event, skipping it", "subject", msg.Subject(), "err", err)
			msg.Term()
			return
		}
		meta, err := msg.Metadata()
		if err != nil {
			return
		}
		fmt.Printf("#%d %s %s about %s (delivery %d)\n", meta.Sequence.Stream,
			evt.OccurredAt.Format(time.TimeOnly), evt.Type, evt.AggregateID, meta.NumDelivered)
		msg.Ack()
	})
	if err != nil {
		return fmt.Errorf("consume: %w", err)
	}
	logger.Info("waiting for wallet events", "consumer", "eventlog")
	<-ctx.Done()
	cc.Stop()
	return nil
}
