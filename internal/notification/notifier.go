package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/fiston-user/payments/internal/platform/events"
	"github.com/google/uuid"
)

// A Notifier turns events into messages, and sends each one once.
type Notifier struct {
	store       *Store
	users       Users
	sender      Sender
	logger      *slog.Logger
	MaxAttempts int           // deliveries of a message, before it's a dead letter
	Backoff     time.Duration // the first retry's delay
}

// NewNotifier returns a Notifier with sensible defaults.
func NewNotifier(store *Store, users Users, sender Sender, logger *slog.Logger) *Notifier {
	return &Notifier{
		store: store, users: users, sender: sender, logger: logger,
		MaxAttempts: 5, Backoff: time.Second,
	}
}

// Notify sends the message evt deserves, unless it was sent before. Calling
// it again with the same event is safe: that's what makes us idempotent.
func (n *Notifier) Notify(ctx context.Context, evt events.Event) error {
	m, ok, err := compose(evt)
	if err != nil || !ok {
		return err
	}
	if m.To, err = n.users.Email(ctx, m.UserID); err != nil {
		return err
	}
	sent, err := n.store.Deliver(ctx, m, func() error { return n.sender.Send(ctx, m) })
	if err != nil {
		return err
	}
	if !sent {
		n.logger.InfoContext(ctx, "already sent, skipping", "type", evt.Type)
		return nil
	}
	n.logger.InfoContext(ctx, "notification sent", "type", evt.Type, "to", m.To)
	return nil
}

// process decodes a message's event, and notifies about it.
func (n *Notifier) process(ctx context.Context, data []byte) error {
	var evt events.Event
	if err := json.Unmarshal(data, &evt); err != nil {
		return Permanent(fmt.Errorf("decode event: %w", err))
	}
	if evt.ID == uuid.Nil {
		return Permanent(fmt.Errorf("event %q has no ID", evt.Type))
	}
	return n.Notify(ctx, evt)
}
