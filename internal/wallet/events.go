package wallet

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fiston-user/payments/internal/platform/events"
	"github.com/fiston-user/payments/internal/platform/telemetry"
	"github.com/fiston-user/payments/internal/wallet/walletdb"
	"github.com/fiston-user/payments/internal/wallet/walletevents"
	"github.com/google/uuid"
)

// enqueue writes an event to the outbox with q, which must be bound to the
// transaction that made it happen: the event exists if, and only if, that
// transaction commits. The outbox relay publishes it later.
func enqueue(ctx context.Context, q *walletdb.Queries, typ string, aggregate uuid.UUID, data any) error {
	evt, err := events.New(typ, aggregate, data)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("encode event %s: %w", typ, err)
	}
	err = q.InsertOutboxEvent(ctx, walletdb.InsertOutboxEventParams{
		EventID: evt.ID,
		Subject: walletevents.Subject(typ),
		Payload: payload,
		// The trace this event belongs to: the relay continues it.
		TraceContext: telemetry.Carry(ctx),
	})
	if err != nil {
		return fmt.Errorf("enqueue event %s: %w", typ, err)
	}
	return nil
}

// eventWallet describes a wallet the way events do.
func eventWallet(acc Account) walletevents.Wallet {
	w := walletevents.Wallet{ID: acc.ID, Currency: acc.Currency}
	if acc.OwnerID != nil {
		w.OwnerID = *acc.OwnerID
	}
	return w
}
