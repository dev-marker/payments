package wallet_test

import (
	"errors"
	"testing"

	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/fiston-user/payments/internal/wallet"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newPool connects to the package's Postgres. It's an integration test: with
// -short, it's skipped.
func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	pool, err := postgres.NewPool(t.Context(), postgresURL, 4)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	// Whatever a test did, the books must balance when it's over.
	t.Cleanup(func() { checkBooks(t, pool) })
	return pool
}

// newStore returns a Store on the test database.
func newStore(t *testing.T) *wallet.Store {
	t.Helper()
	return wallet.NewStore(newPool(t))
}

// newAccount opens a USD wallet for a new owner, so every test has its own data.
func newAccount(t *testing.T, s *wallet.Store, name string, side wallet.Direction) uuid.UUID {
	t.Helper()
	owner := uuid.New()
	acc, err := s.CreateAccount(t.Context(), wallet.NewAccount{
		Kind:          wallet.KindWallet,
		OwnerID:       &owner,
		Name:          name,
		Currency:      "USD",
		NormalBalance: side,
	})
	if err != nil {
		t.Fatalf("create account %s: %v", name, err)
	}
	return acc.ID
}

// balance returns an account's current balance in cents.
func balance(t *testing.T, s *wallet.Store, id uuid.UUID) int64 {
	t.Helper()
	b, err := s.Balance(t.Context(), id)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	return b.Balance
}

func TestPostTransaction(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	bank := newAccount(t, s, "bank", wallet.Debit)
	alice := newAccount(t, s, "alice", wallet.Credit)

	tx, err := s.PostTransaction(t.Context(), wallet.NewTransaction{
		Description: "alice deposits $50",
		Postings: []wallet.Posting{
			{AccountID: bank, Direction: wallet.Debit, Amount: 5000, Currency: "USD"},
			{AccountID: alice, Direction: wallet.Credit, Amount: 5000, Currency: "USD"},
		},
	})
	if err != nil {
		t.Fatalf("post transaction: %v", err)
	}

	if got := balance(t, s, bank); got != 5000 {
		t.Errorf("bank balance = %d, want 5000", got)
	}
	if got := balance(t, s, alice); got != 5000 {
		t.Errorf("alice balance = %d, want 5000", got)
	}

	entries, err := s.Entries(t.Context(), alice, 0, 10)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 1 || entries[0].TransactionID != tx.ID {
		t.Errorf("alice has %d entries, want 1 in transaction %s", len(entries), tx.ID)
	}
}

func TestPostTransactionRejectsUnbalanced(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	alice := newAccount(t, s, "alice", wallet.Credit)
	bob := newAccount(t, s, "bob", wallet.Credit)

	// Alice pays $20, but Bob only receives $15: five dollars would vanish.
	_, err := s.PostTransaction(t.Context(), wallet.NewTransaction{
		Description: "alice pays bob $20",
		Postings: []wallet.Posting{
			{AccountID: alice, Direction: wallet.Debit, Amount: 2000, Currency: "USD"},
			{AccountID: bob, Direction: wallet.Credit, Amount: 1500, Currency: "USD"},
		},
	})

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("got error %v, want a check_violation (23514) from Postgres", err)
	}
	t.Logf("rejected by Postgres: %s", pgErr.Message)

	// Atomic: the entries inserted before the failed commit were rolled back too.
	if got := balance(t, s, alice); got != 0 {
		t.Errorf("alice balance = %d, want 0", got)
	}
	if got := balance(t, s, bob); got != 0 {
		t.Errorf("bob balance = %d, want 0", got)
	}
}

func TestAccountNotFound(t *testing.T) {
	t.Parallel()
	s := newStore(t)

	_, err := s.Account(t.Context(), uuid.New())
	if !errors.Is(err, wallet.ErrAccountNotFound) {
		t.Fatalf("got error %v, want ErrAccountNotFound", err)
	}
}
