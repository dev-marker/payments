package wallet

import (
	"context"
	"fmt"

	"github.com/fiston-user/payments/internal/wallet/walletdb"
	"github.com/google/uuid"
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
// transaction. It commits when fn returns nil, and rolls back otherwise.
func (s *Store) inTx(ctx context.Context, fn func(q *walletdb.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
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
		journal, err := q.CreateTransaction(ctx, nt.Description)
		if err != nil {
			return fmt.Errorf("insert transaction: %w", err)
		}
		posted.Transaction = journal

		for _, p := range nt.Postings {
			entry, err := q.CreateEntry(ctx, walletdb.CreateEntryParams{
				TransactionID: journal.ID,
				AccountID:     p.AccountID,
				Direction:     p.Direction,
				Amount:        p.Amount,
				Currency:      p.Currency,
			})
			if err != nil {
				return fmt.Errorf("insert entry: %w", err)
			}
			posted.Entries = append(posted.Entries, entry)
		}
		return nil
	})
	if err != nil {
		return Transaction{}, fmt.Errorf("post transaction: %w", err)
	}
	return posted, nil
}
