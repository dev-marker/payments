package wallet_test

import (
	"testing"
	"time"

	"github.com/fiston-user/payments/internal/wallet"
)

// TestStatementPages posts more deposits than fit on two pages of a statement,
// in one statement, so they all have the same created_at: keyset pagination's
// hardest case, where only the id tells them apart. Every one must come back,
// once, in order, with a balance that adds up.
func TestStatementPages(t *testing.T) {
	t.Parallel()
	pool := newPool(t)
	store := wallet.NewStore(pool)
	svc := wallet.NewService(store)
	w := newWallet(t, svc, 0)
	settlement, err := store.SystemAccount(t.Context(), wallet.SettlementAccount, "USD")
	if err != nil {
		t.Fatal(err)
	}
	// 1,001 deposits of a cent: two full pages, and one more line.
	_, err = pool.Exec(t.Context(), `WITH txs AS (
			INSERT INTO transactions (description)
			SELECT 'deposit' FROM generate_series(1, 1001) RETURNING id)
		INSERT INTO entries (transaction_id, account_id, direction, amount, currency)
		SELECT id, account, direction::entry_direction, 1, 'USD'
		FROM txs, (VALUES ($1::uuid, 'credit'), ($2::uuid, 'debit')) AS e (account, direction)`,
		w, settlement.ID)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC().Truncate(24 * time.Hour)
	lines, err := svc.Statement(t.Context(), w, day, day)
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	for line, err := range lines {
		if err != nil {
			t.Fatal(err)
		}
		if n++; line.Balance != n {
			t.Fatalf("line %d: balance %d, want %d", n, line.Balance, n)
		}
	}
	if n != 1001 {
		t.Errorf("%d lines, want 1001", n)
	}
}
