// Package natsx connects services to NATS, and sets up JetStream streams.
package natsx

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Connect connects to the NATS server at url. The name shows up in the
// server's list of connections, so we can tell who's who. Once connected, the
// client reconnects by itself, forever, and logs every change.
func Connect(url, name string, logger *slog.Logger) (*nats.Conn, error) {
	nc, err := nats.Connect(url,
		nats.Name(name),
		nats.Timeout(2*time.Second),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
		// While disconnected, publishing fails right away, instead of waiting
		// in a buffer in memory that dies with the process.
		nats.ReconnectBufSize(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				logger.Warn("nats disconnected", "err", err)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logger.Info("nats reconnected", "url", nc.ConnectedUrl())
		}),
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			if sub != nil {
				logger.Error("nats error", "subject", sub.Subject, "err", err)
				return
			}
			logger.Error("nats error", "err", err)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("nats: connect to %s: %w", url, err)
	}
	return nc, nil
}

// Connected is a readiness check: is nc connected to NATS, right now?
func Connected(nc *nats.Conn) func(context.Context) error {
	return func(context.Context) error {
		if !nc.IsConnected() {
			return fmt.Errorf("nats: %s", nc.Status())
		}
		return nil
	}
}

// EnsureStream creates a stream, or updates it to cfg if it exists. Every
// instance of a service can call it at startup, any number of times, and a
// changed config rolls out with the next deploy.
func EnsureStream(ctx context.Context, js jetstream.JetStream, cfg jetstream.StreamConfig) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stream, err := js.CreateOrUpdateStream(ctx, cfg)
	if err != nil {
		return fmt.Errorf("nats: stream %s: %w", cfg.Name, err)
	}
	slog.InfoContext(ctx, "stream ready", "stream", cfg.Name, "subjects", cfg.Subjects,
		"messages", stream.CachedInfo().State.Msgs)
	return nil
}

// HeaderCarrier lets OpenTelemetry read and write a message's headers: Get
// and Set come with nats.Header, and Keys lists them.
type HeaderCarrier struct{ nats.Header }

func (c HeaderCarrier) Keys() []string {
	return slices.Collect(maps.Keys(c.Header))
}
