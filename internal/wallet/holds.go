package wallet

import (
	"context"
	"errors"
	"fmt"

	"github.com/fiston-user/payments/internal/platform/apperr"
	"github.com/fiston-user/payments/internal/wallet/walletdb"
	"github.com/fiston-user/payments/internal/wallet/walletevents"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// HoldsAccount is the system account where held money waits.
const HoldsAccount = "holds"

type (
	Hold       = walletdb.Hold
	HoldStatus = walletdb.HoldStatus
)

const (
	HoldHeld     = walletdb.HoldStatusHeld
	HoldCaptured = walletdb.HoldStatusCaptured
	HoldReleased = walletdb.HoldStatusReleased
)

var (
	ErrWalletFrozen     = apperr.NewRule("wallet_frozen", "wallet is frozen")
	ErrCurrencyMismatch = apperr.NewRule("currency_mismatch", "wallets have different currencies")
	ErrHoldSettled      = apperr.NewRule("hold_settled", "hold was already captured or released")
	ErrHoldNotFound     = fmt.Errorf("hold %w", apperr.ErrNotFound)
)

// Hold sets money aside from a wallet, as hold id. Calling it again is safe.
func (s *Service) Hold(ctx context.Context, id, walletID uuid.UUID, amount Money) (Hold, error) {
	var v apperr.ValidationError
	checkAmount(&v, amount.Amount)
	w, err := s.Wallet(ctx, walletID)
	if err != nil {
		return Hold{}, err
	}
	if amount.Currency != w.Currency {
		v.Add("currency", "must match the wallet's currency, "+w.Currency)
	}
	if err := v.Err(); err != nil {
		return Hold{}, err
	}
	if w.Frozen {
		return Hold{}, fmt.Errorf("wallet %s: %w", w.ID, ErrWalletFrozen)
	}
	return s.store.PlaceHold(ctx, id, w, amount.Amount)
}

// Capture pays hold id into anyone's wallet.
func (s *Service) Capture(ctx context.Context, id, to uuid.UUID) (Hold, error) {
	dst, err := (&Service{store: s.store}).Wallet(ctx, to)
	if err != nil {
		return Hold{}, err
	}
	return s.store.SettleHold(ctx, id, &dst)
}

// Release gives hold id back to the wallet it came from.
func (s *Service) Release(ctx context.Context, id uuid.UUID) (Hold, error) {
	return s.store.SettleHold(ctx, id, nil)
}

// PlaceHold moves amount from w into the holds account, as hold id, once.
func (s *Store) PlaceHold(ctx context.Context, id uuid.UUID, w Account, amount int64) (Hold, error) {
	var h Hold
	err := s.inTx(ctx, func(q *walletdb.Queries) error {
		if _, err := q.LockAccount(ctx, w.ID); err != nil {
			return fmt.Errorf("lock account %s: %w", w.ID, err)
		}
		var err error
		h, err = q.GetHold(ctx, id)
		if err == nil {
			if h.WalletID != w.ID || h.Amount != amount {
				return fmt.Errorf("%w: hold %s has different terms", apperr.ErrConflict, id)
			}
			return nil // placed before: nothing to do
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("get hold %s: %w", id, err)
		}
		b, err := q.GetAccountBalance(ctx, w.ID)
		if err != nil {
			return fmt.Errorf("get balance %s: %w", w.ID, err)
		}
		if b.Balance < amount {
			return fmt.Errorf("wallet %s: %w", w.ID, ErrInsufficientFunds)
		}

		holds, err := holdsAccount(ctx, q, w.Currency)
		if err != nil {
			return err
		}
		posted, err := insertTransaction(ctx, q, NewTransaction{
			Description: "hold",
			Postings: []Posting{
				{AccountID: w.ID, Direction: Debit, Amount: amount, Currency: w.Currency},
				{AccountID: holds.ID, Direction: Credit, Amount: amount, Currency: w.Currency},
			},
		})
		if err != nil {
			return err
		}
		h, err = q.CreateHold(ctx, walletdb.CreateHoldParams{
			ID: id, WalletID: w.ID, Amount: amount, Currency: w.Currency, HoldTxID: posted.ID,
		})
		return err
	})
	if err != nil {
		return Hold{}, fmt.Errorf("place hold: %w", err)
	}
	return h, nil
}

// SettleHold pays hold id into wallet to, or back to its own if to is nil.
func (s *Store) SettleHold(ctx context.Context, id uuid.UUID, to *Account) (Hold, error) {
	status := HoldReleased
	if to != nil {
		status = HoldCaptured
	}
	var h Hold
	err := s.inTx(ctx, func(q *walletdb.Queries) error {
		var err error
		h, err = q.LockHold(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrHoldNotFound
		}
		if err != nil {
			return fmt.Errorf("lock hold %s: %w", id, err)
		}
		if h.Status == status && (to == nil || *h.SettledTo == to.ID) {
			return nil // settled before, the same way
		}
		if h.Status != HoldHeld {
			return fmt.Errorf("hold %s is %s: %w", id, h.Status, ErrHoldSettled)
		}

		src, err := q.GetAccount(ctx, h.WalletID)
		if err != nil {
			return fmt.Errorf("get account %s: %w", h.WalletID, err)
		}
		dst := src // a release always works, even into a frozen wallet
		if to != nil {
			dst = *to
			switch {
			case dst.Currency != h.Currency:
				return fmt.Errorf("wallet %s: %w", dst.ID, ErrCurrencyMismatch)
			case dst.Frozen:
				return fmt.Errorf("wallet %s: %w", dst.ID, ErrWalletFrozen)
			}
		}
		holds, err := holdsAccount(ctx, q, h.Currency)
		if err != nil {
			return err
		}
		posted, err := insertTransaction(ctx, q, NewTransaction{
			Description: "hold " + string(status),
			Postings: []Posting{
				{AccountID: holds.ID, Direction: Debit, Amount: h.Amount, Currency: h.Currency},
				{AccountID: dst.ID, Direction: Credit, Amount: h.Amount, Currency: h.Currency},
			},
		})
		if err != nil {
			return err
		}
		h, err = q.SettleHold(ctx, walletdb.SettleHoldParams{
			ID: id, Status: status, SettledTo: &dst.ID, SettleTxID: &posted.ID,
		})
		if err != nil || status != HoldCaptured {
			return err
		}
		return enqueue(ctx, q, walletevents.TransferCompleted, src.ID, walletevents.Transfer{
			TransactionID: posted.ID,
			From:          eventWallet(src),
			To:            eventWallet(dst),
			Amount:        h.Amount,
		})
	})
	if err != nil {
		return Hold{}, fmt.Errorf("settle hold: %w", err)
	}
	return h, nil
}

// holdsAccount returns the system account where held money waits.
func holdsAccount(ctx context.Context, q *walletdb.Queries, currency string) (Account, error) {
	acc, err := q.GetSystemAccount(ctx, walletdb.GetSystemAccountParams{
		Name:     HoldsAccount,
		Currency: currency,
	})
	if err != nil {
		return Account{}, fmt.Errorf("holds account %s: %w", currency, err)
	}
	return acc, nil
}
