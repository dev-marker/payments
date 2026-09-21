package wallet_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/fiston-user/payments/internal/platform/events"
	"github.com/fiston-user/payments/internal/wallet"
	"github.com/fiston-user/payments/internal/wallet/walletevents"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// outboxEvents returns the events in the outbox about a wallet, oldest first.
func outboxEvents(t *testing.T, pool *pgxpool.Pool, wallet uuid.UUID) []events.Event {
	t.Helper()
	rows, _ := pool.Query(t.Context(), `
		SELECT payload FROM outbox
		WHERE payload->>'aggregate_id' = $1
		ORDER BY seq`, wallet.String())
	payloads, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	var evts []events.Event
	for _, p := range payloads {
		var e events.Event
		if err := json.Unmarshal(p, &e); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		evts = append(evts, e)
	}
	return evts
}

func TestEventsCommitWithTheMoney(t *testing.T) {
	t.Parallel()
	pool := newPool(t)
	svc := wallet.NewService(wallet.NewStore(pool))
	alice, bob := newWallet(t, svc, 1000), newWallet(t, svc, 0)

	// A transfer that commits.
	if _, err := svc.Transfer(t.Context(), alice.String(), bob.String(), usd(300)); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	// One that's refused: Alice doesn't have $50.
	if _, err := svc.Transfer(t.Context(), alice.String(), bob.String(), usd(5000)); err == nil {
		t.Fatal("transfer of $50 went through, want insufficient funds")
	}
	// And one that works, in a transaction that then rolls back.
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := svc.WithTx(tx).Transfer(t.Context(), alice.String(), bob.String(), usd(200)); err != nil {
		t.Fatalf("transfer in tx: %v", err)
	}
	tx.Rollback(t.Context())

	evts := outboxEvents(t, pool, alice)
	types := make([]string, len(evts))
	for i, e := range evts {
		types[i] = e.Type
	}
	want := []string{walletevents.WalletCreated, walletevents.DepositCompleted, walletevents.TransferCompleted}
	if !slices.Equal(types, want) {
		t.Fatalf("events = %v, want %v", types, want)
	}
	var tr walletevents.Transfer
	if err := json.Unmarshal(evts[2].Data, &tr); err != nil {
		t.Fatalf("decode transfer: %v", err)
	}
	if tr.Amount != 300 || tr.From.ID != alice || tr.To.ID != bob {
		t.Errorf("transfer event = %+v, want $3 from alice to bob", tr)
	}
}
