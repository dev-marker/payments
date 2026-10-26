package wallet_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/fiston-user/payments/internal/wallet"
	"github.com/jackc/pgx/v5"
)

// TestReconcileFindsTampering bumps a deposit by ten dollars behind the
// triggers' back, as a hand-written fix might, and expects reconciliation to
// name the transaction. It all happens in a transaction that's rolled back,
// so no other test ever sees it.
func TestReconcileFindsTampering(t *testing.T) {
	t.Parallel()
	pool := newPool(t)
	svc := wallet.NewService(wallet.NewStore(pool))
	deposit, err := svc.Deposit(t.Context(), newWallet(t, svc, 0), usd(5000))
	if err != nil {
		t.Fatalf("deposit: %v", err)
	}
	tx, err := pool.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	store := wallet.NewStore(pool).WithTx(tx)
	if r, err := store.Reconcile(t.Context()); err != nil || len(r.Discrepancies) > 0 {
		t.Fatalf("before: Reconcile = %+v, %v; want no discrepancies", r, err)
	}

	// Replica mode is for replication tools: it fires no triggers, so nothing
	// checks this UPDATE. Only a superuser can switch it on.
	_, err = tx.Exec(t.Context(), "SET LOCAL session_replication_role = replica")
	if err == nil {
		_, err = tx.Exec(t.Context(), `UPDATE entries SET amount = amount + 1000
			WHERE transaction_id = $1 AND direction = 'credit'`, deposit.ID)
	}
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.Reconcile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range r.Discrepancies {
		got = append(got, fmt.Sprintf("%s %s %s %d %d", d.Check, d.Ref, d.Currency, d.Expected, d.Actual))
	}
	want := []string{
		"trial_balance all entries USD 0 -1000",
		fmt.Sprintf("unbalanced_transaction %s USD 0 -1000", deposit.ID),
	}
	if !slices.Equal(got, want) {
		t.Errorf("Reconcile found\n%q\nwant\n%q", got, want)
	}
}
