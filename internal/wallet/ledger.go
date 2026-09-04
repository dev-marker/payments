package wallet

import (
	"context"
	"fmt"

	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/fiston-user/payments/internal/wallet/walletdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Posting is one line of a transaction: money in or out of one account.
type Posting struct {
	AccountID uuid.UUID
	Direction Direction
	Amount    int64
	Currency  string
}

// NewTransaction is a transaction we want to record.
type NewTransaction struct {
	Description string
	Postings    []Posting
}

// Transaction is a posted journal entry, together with its entries.
type Transaction struct {
	walletdb.Transaction
	Entries []Entry `json:"entries"`
}

// inTx runs fn inside a database transaction, handing it queries bound to that
// transaction. It commits when fn returns nil, and rolls back otherwise. If the
// transaction collides with another one, all of it runs again, fn included.
func (s *Store) inTx(ctx context.Context, fn func(q *walletdb.Queries) error) error {
	return postgres.Retry(ctx, func() error {
		return s.tryTx(ctx, fn)
	})
}

// tryTx makes one attempt at running fn inside a database transaction.
func (s *Store) tryTx(ctx context.Context, fn func(q *walletdb.Queries) error) error {
	// Our row locks rely on READ COMMITTED: every statement sees all the work
	// committed before it started, even work we just waited for. Ask for it,
	// so a different default on the server can't quietly break that.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	// Rollback is a no-op once the transaction has committed, so deferring it
	// covers every early return, and even a panic, in one line.
	defer tx.Rollback(ctx)

	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	// Deferred constraints, like our balance check, run here, at commit.
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// PostTransaction records a transaction and all of its entries atomically:
// either every entry is stored, or none are. Postgres rejects the whole thing
// at commit if its debits and credits don't balance.
func (s *Store) PostTransaction(ctx context.Context, nt NewTransaction) (Transaction, error) {
	var posted Transaction
	err := s.inTx(ctx, func(q *walletdb.Queries) error {
		var err error
		posted, err = insertTransaction(ctx, q, nt)
		return err
	})
	if err != nil {
		return Transaction{}, fmt.Errorf("post transaction: %w", err)
	}
	return posted, nil
}

// insertTransaction inserts a transaction and its entries with q, which must
// be bound to a database transaction: on its own, it isn't atomic.
func insertTransaction(ctx context.Context, q *walletdb.Queries, nt NewTransaction) (Transaction, error) {
	journal, err := q.CreateTransaction(ctx, nt.Description)
	if err != nil {
		return Transaction{}, fmt.Errorf("insert transaction: %w", err)
	}
	posted := Transaction{Transaction: journal}
	for _, p := range nt.Postings {
		entry, err := q.CreateEntry(ctx, walletdb.CreateEntryParams{
			TransactionID: journal.ID,
			AccountID:     p.AccountID,
			Direction:     p.Direction,
			Amount:        p.Amount,
			Currency:      p.Currency,
		})
		if err != nil {
			return Transaction{}, fmt.Errorf("insert entry: %w", err)
		}
		posted.Entries = append(posted.Entries, entry)
	}
	return posted, nil
}
