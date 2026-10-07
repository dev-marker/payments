package wallet_test

import (
	"context"
	"math/rand/v2"
	"testing"

	"github.com/fiston-user/payments/internal/wallet"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// problems lists what's wrong with the whole ledger, in one query, so in one
// snapshot: a currency whose debits don't equal its credits, a wallet below
// zero, or a holds account without exactly the money of the open holds.
const problems = `
SELECT 'unbalanced: ' || currency FROM entries GROUP BY currency
HAVING sum(CASE direction WHEN 'debit' THEN amount ELSE -amount END) <> 0
UNION ALL
SELECT 'overdrawn: ' || b.name FROM account_balances b
JOIN accounts a ON a.id = b.account_id
WHERE a.kind = 'wallet' AND b.balance < 0
UNION ALL
SELECT 'holds account off: ' || b.currency FROM account_balances b
JOIN accounts a ON a.id = b.account_id
WHERE a.kind = 'system' AND a.name = 'holds' AND b.balance <> (
	SELECT coalesce(sum(amount), 0) FROM holds h
	WHERE h.currency = b.currency AND h.status = 'held')`

// checkBooks fails the test if the ledger has any problems. It runs when the
// test is over, and t.Context() is done, so it brings a context of its own.
func checkBooks(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	rows, _ := pool.Query(context.Background(), problems)
	found, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("check the books: %v", err)
	}
	for _, p := range found {
		t.Error(p)
	}
}

func TestBooksBalanceUnderLoad(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	svc := wallet.NewService(s)
	wallets := make([]uuid.UUID, 5)
	for i := range wallets {
		wallets[i] = newWallet(t, svc, 10_000)
	}

	// 300 payments at once between five wallets: half of them transfers, and
	// half the saga's way, a hold that's then captured or released.
	errs := concurrently(300, func() error {
		i, j := rand.IntN(5), rand.IntN(4)
		if j >= i {
			j++ // any wallet but i
		}
		from, to, amount := wallets[i], wallets[j], usd(rand.Int64N(2000)+1)
		if rand.IntN(2) == 0 {
			_, err := svc.Transfer(t.Context(), from.String(), to.String(), amount)
			return err
		}
		hold := uuid.New()
		if _, err := svc.Hold(t.Context(), hold, from, amount); err != nil {
			return err
		}
		var err error
		if rand.IntN(2) == 0 {
			_, err = svc.Capture(t.Context(), hold, to)
		} else {
			_, err = svc.Release(t.Context(), hold)
		}
		return err
	})
	ok, refused := tally(t, errs)
	t.Logf("%d payments went through, %d refused for insufficient funds", ok, refused)

	var total int64
	for _, w := range wallets {
		total += balance(t, s, w)
	}
	if total != 5*10_000 {
		t.Errorf("the wallets hold %d, want %d", total, 5*10_000)
	}
}
