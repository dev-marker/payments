package wallet

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/fiston-user/payments/internal/fx"
	"github.com/fiston-user/payments/internal/wallet/walletdb"
	"github.com/google/uuid"
)

// A StatementLine is one entry on a wallet's statement, and the balance after it.
type StatementLine struct {
	PostedAt      time.Time
	TransactionID uuid.UUID
	Description   string
	Amount        int64 // money in is positive, money out negative
	Balance       int64
	Currency      string
}

// StatementHeader names the columns of Record.
var StatementHeader = []string{"date", "description", "amount", "balance", "currency", "transaction_id"}

// Record is l as a CSV record: amounts as decimals, in the currency's own
// minor units, and the description as text that's safe in a spreadsheet.
func (l StatementLine) Record() ([]string, error) {
	amount, err := fx.Format(l.Amount, l.Currency)
	if err != nil {
		return nil, err
	}
	balance, err := fx.Format(l.Balance, l.Currency)
	if err != nil {
		return nil, err
	}
	return []string{l.PostedAt.UTC().Format(time.RFC3339), cell(l.Description), amount, balance,
		l.Currency, l.TransactionID.String()}, nil
}

// cell keeps text from being run as a formula when a CSV file is opened in a
// spreadsheet: a cell that starts with =, +, -, @, a tab or a carriage
// return gets a ' in front, which the spreadsheet shows as text.
func cell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// statementPage is how many entries a statement reads at a time.
const statementPage = 500

// Statement returns acc's entries posted in [since, until), oldest first,
// each with the balance after it. They're read a page at a time, in one
// snapshot: memory stays flat, and the balances add up.
func (s *Store) Statement(
	ctx context.Context, acc Account, since, until time.Time,
) iter.Seq2[StatementLine, error] {
	return func(yield func(StatementLine, error) bool) {
		fail := func(err error) { yield(StatementLine{}, fmt.Errorf("statement: %w", err)) }
		tx, err := s.pool.BeginTx(ctx, snapshot)
		if err != nil {
			fail(err)
			return
		}
		defer tx.Rollback(ctx)
		q := s.q.WithTx(tx)
		balance, err := q.BalanceBefore(ctx, walletdb.BalanceBeforeParams{AccountID: acc.ID, Before: since})
		if err != nil {
			fail(err)
			return
		}
		page := walletdb.StatementPageParams{
			AccountID: acc.ID, Since: since, Until: until, PageSize: statementPage,
		}
		for {
			entries, err := q.StatementPage(ctx, page)
			if err != nil {
				fail(err)
				return
			}
			for _, e := range entries {
				amount := e.Amount
				if e.Direction != acc.NormalBalance {
					amount = -amount
				}
				balance += amount
				line := StatementLine{e.CreatedAt, e.TransactionID, e.Description,
					amount, balance, acc.Currency}
				if !yield(line, nil) {
					return // the caller stopped
				}
				page.AfterID = e.ID // the next page starts after this entry
			}
			if len(entries) < statementPage {
				return
			}
		}
	}
}

// Statement returns the lines of a wallet's statement for the days from
// first to last, both included, in UTC.
func (s *Service) Statement(ctx context.Context, id uuid.UUID, first, last time.Time) (
	iter.Seq2[StatementLine, error], error,
) {
	w, err := s.Wallet(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.store.Statement(ctx, w, first, last.AddDate(0, 0, 1)), nil
}

// TrialBalance returns every ledger account's debits and credits before a
// moment, with a total per currency.
func (s *Store) TrialBalance(ctx context.Context, before time.Time) ([]walletdb.TrialBalanceRow, error) {
	lines, err := s.q.TrialBalance(ctx, before)
	if err != nil {
		return nil, fmt.Errorf("trial balance: %w", err)
	}
	return lines, nil
}

// TrialBalance is the trial balance before a moment: for us, never for customers.
func (s *Service) TrialBalance(ctx context.Context, before time.Time) ([]walletdb.TrialBalanceRow, error) {
	return s.store.TrialBalance(ctx, before)
}
