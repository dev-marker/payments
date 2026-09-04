package wallet

import (
	"bytes"
	"context"
	"fmt"

	"github.com/fiston-user/payments/internal/platform/apperr"
	"github.com/fiston-user/payments/internal/wallet/walletdb"
	"github.com/google/uuid"
)

// ErrInsufficientFunds is returned when a transfer would take a wallet below
// zero. Clients get a 422, with the code insufficient_funds.
var ErrInsufficientFunds = apperr.NewRule("insufficient_funds", "insufficient funds")

// Transfer moves amount from one account to another, as one balanced
// transaction, and fails with ErrInsufficientFunds if from can't cover it.
func (s *Store) Transfer(ctx context.Context, from, to uuid.UUID, amount int64, currency string) (Transaction, error) {
	var posted Transaction
	err := s.inTx(ctx, func(q *walletdb.Queries) error {
		// Lock both accounts until we commit. Another transfer that touches
		// either one waits right here, so the balance below stays true. Every
		// transfer locks the lower ID first, so two transfers can never each
		// hold the lock the other one is waiting for: no deadlocks.
		first, second := from, to
		if bytes.Compare(from[:], to[:]) > 0 {
			first, second = to, from
		}
		for _, id := range []uuid.UUID{first, second} {
			if _, err := q.LockAccount(ctx, id); err != nil {
				return fmt.Errorf("lock account %s: %w", id, err)
			}
		}

		b, err := q.GetAccountBalance(ctx, from)
		if err != nil {
			return fmt.Errorf("get balance %s: %w", from, err)
		}
		if b.Balance < amount {
			return fmt.Errorf("wallet %s: %w", from, ErrInsufficientFunds)
		}

		posted, err = insertTransaction(ctx, q, NewTransaction{
			Description: "transfer",
			Postings: []Posting{
				{AccountID: from, Direction: Debit, Amount: amount, Currency: currency},
				{AccountID: to, Direction: Credit, Amount: amount, Currency: currency},
			},
		})
		return err
	})
	if err != nil {
		return Transaction{}, fmt.Errorf("transfer: %w", err)
	}
	return posted, nil
}
