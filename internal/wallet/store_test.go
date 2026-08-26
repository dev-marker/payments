package wallet_test

import (
	"errors"
	"os"
	"testing"

	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/fiston-user/payments/internal/wallet"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// newStore connects to the database in WALLET_TEST_DATABASE_URL. Without it the
// test is skipped, so go test ./... still passes on a machine without Docker.
func newStore(t *testing.T) *wallet.Store {
	t.Helper()
	dsn := os.Getenv("WALLET_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("WALLET_TEST_DATABASE_URL not set, skipping integration test")
	}
	pool, err := postgres.NewPool(t.Context(), dsn, 4)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return wallet.NewStore(pool)
}

// newAccount opens a fresh USD account, so every test works on its own data.
func newAccount(t *testing.T, s *wallet.Store, name string, side wallet.Direction) uuid.UUID {
	t.Helper()
	acc, err := s.CreateAccount(t.Context(), wallet.NewAccount{
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
