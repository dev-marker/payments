// Package wallet is the wallet service: accounts, balances and the ledger.
package wallet

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/fiston-user/payments/internal/platform/apperr"
	"github.com/fiston-user/payments/internal/wallet/walletdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The generated models double as our domain types for now. The aliases mean
// callers only ever import package wallet, never walletdb.
type (
	Account        = walletdb.Account
	AccountBalance = walletdb.AccountBalance
	Entry          = walletdb.Entry
	NewAccount     = walletdb.CreateAccountParams
	Direction      = walletdb.EntryDirection
)

const (
	Debit  = walletdb.EntryDirectionDebit
	Credit = walletdb.EntryDirectionCredit
)

// ErrAccountNotFound is returned when an account does not exist. It wraps
// apperr.ErrNotFound, so the HTTP layer answers with a 404.
var ErrAccountNotFound = fmt.Errorf("account %w", apperr.ErrNotFound)

// Store is the wallet's data access layer, often called a repository. It wraps
// the sqlc queries, and owns database transactions.
type Store struct {
	pool *pgxpool.Pool
	q    *walletdb.Queries
}

// NewStore returns a Store that runs its queries on pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: walletdb.New(pool)}
}

// CreateAccount opens a new account. OwnerID is nil for system accounts.
func (s *Store) CreateAccount(ctx context.Context, a NewAccount) (Account, error) {
	acc, err := s.q.CreateAccount(ctx, a)
	if err != nil {
		return Account{}, fmt.Errorf("create account: %w", err)
	}
	return acc, nil
}

// Account returns the account with the given id.
func (s *Store) Account(ctx context.Context, id uuid.UUID) (Account, error) {
	acc, err := s.q.GetAccount(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("get account %s: %w", id, err)
	}
	return acc, nil
}

// Balance returns an account's balance, derived from its entries by the
// account_balances view. Amounts are minor units of the account's currency.
func (s *Store) Balance(ctx context.Context, id uuid.UUID) (AccountBalance, error) {
	b, err := s.q.GetAccountBalance(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountBalance{}, ErrAccountNotFound
	}
	if err != nil {
		return AccountBalance{}, fmt.Errorf("get balance %s: %w", id, err)
	}
	return b, nil
}

// Entries returns a page of an account's entries, newest first. Pass 0 as
// before for the first page, then the id of the last entry you received.
func (s *Store) Entries(ctx context.Context, id uuid.UUID, before int64, limit int32) ([]Entry, error) {
	if before <= 0 {
		before = math.MaxInt64
	}
	entries, err := s.q.ListEntriesByAccount(ctx, walletdb.ListEntriesByAccountParams{
		AccountID: id,
		BeforeID:  before,
		PageSize:  limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list entries %s: %w", id, err)
	}
	return entries, nil
}
