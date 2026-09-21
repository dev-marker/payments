package wallet

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/fiston-user/payments/internal/platform/apperr"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SettlementAccount names the system account that funds deposits: the money
// we hold at our bank, one account per currency.
const SettlementAccount = "settlement"

// MaxAmount caps a single deposit or transfer, in minor units: a million
// dollars, or a million euros. It catches typos, and keeps sums far from
// overflowing.
const MaxAmount = 100_000_000

// ErrWalletNotFound is returned when there's no wallet with the given ID.
var ErrWalletNotFound = fmt.Errorf("wallet %w", apperr.ErrNotFound)

// Money is an amount in minor units of a currency, like cents for USD. It's
// an integer, never a float, and it never travels without its currency.
type Money struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// Service is the wallet's business logic. It validates input, enforces the
// rules, and uses the Store to persist the results. It knows nothing about
// HTTP, so every way into the service gets the same rules.
type Service struct {
	store *Store
	user  *uuid.UUID // set by As: the one owner whose wallets we see
}

// NewService returns a Service that keeps its data in store.
func NewService(store *Store) *Service {
	return &Service{store: store}
}

// WithTx returns a Service that does all its work inside tx, the caller's
// database transaction. See Store.WithTx.
func (s *Service) WithTx(tx pgx.Tx) *Service {
	return &Service{store: s.store.WithTx(tx), user: s.user}
}

// As returns a Service that acts for a user: it only sees their wallets. The
// Service without a user sees everything, for our own code, like tests.
func (s *Service) As(user uuid.UUID) *Service {
	return &Service{store: s.store, user: &user}
}

// currencyCode matches the shape of an ISO 4217 code, like USD or EUR.
var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// CreateWallet opens a wallet for an owner, in one currency. An owner can
// have one wallet per currency.
func (s *Service) CreateWallet(ctx context.Context, ownerID, currency string) (Account, error) {
	var v apperr.ValidationError
	owner, err := uuid.Parse(ownerID)
	if err != nil || owner == uuid.Nil {
		v.Add("owner_id", "must be a UUID")
	}
	if !currencyCode.MatchString(currency) {
		v.Add("currency", "must be a three-letter ISO 4217 code, like USD")
	}
	if err := v.Err(); err != nil {
		return Account{}, err
	}

	// We support a currency when we have a settlement account to fund it.
	_, err = s.store.SystemAccount(ctx, SettlementAccount, currency)
	if errors.Is(err, ErrAccountNotFound) {
		v.Add("currency", "unsupported currency")
		return Account{}, v.Err()
	}
	if err != nil {
		return Account{}, err
	}

	acc, err := s.store.CreateAccount(ctx, NewAccount{
		Kind:          KindWallet,
		OwnerID:       &owner,
		Name:          currency + " wallet",
		Currency:      currency,
		NormalBalance: Credit,
	})
	if errors.Is(err, ErrAccountExists) {
		return Account{}, fmt.Errorf("%w: owner already has a wallet in %s", apperr.ErrConflict, currency)
	}
	return acc, err
}

// Wallet returns the wallet with the given ID.
func (s *Service) Wallet(ctx context.Context, id uuid.UUID) (Account, error) {
	acc, err := s.store.Account(ctx, id)
	if errors.Is(err, ErrAccountNotFound) || (err == nil && !s.sees(acc)) {
		// System accounts are ours, and other people's wallets are theirs: to
		// this caller, they don't exist.
		return Account{}, ErrWalletNotFound
	}
	return acc, err
}

// sees reports whether acc is a wallet this Service may see.
func (s *Service) sees(acc Account) bool {
	if acc.Kind != KindWallet {
		return false
	}
	return s.user == nil || (acc.OwnerID != nil && *acc.OwnerID == *s.user)
}

// Balance returns a wallet's balance, derived from its entries.
func (s *Service) Balance(ctx context.Context, id uuid.UUID) (AccountBalance, error) {
	if _, err := s.Wallet(ctx, id); err != nil {
		return AccountBalance{}, err
	}
	return s.store.Balance(ctx, id)
}

// Deposit adds money from outside to a wallet. It posts a balanced
// transaction: a debit to the settlement account, which now holds the money
// at our bank, and a credit to the wallet, which now owes it to the customer.
func (s *Service) Deposit(ctx context.Context, walletID uuid.UUID, amount Money) (Transaction, error) {
	var v apperr.ValidationError
	checkAmount(&v, amount.Amount)

	w, err := s.Wallet(ctx, walletID)
	if err != nil {
		return Transaction{}, err
	}
	// The client must say which currency it means, so 500 can never quietly
	// become 500 yen in a dollar wallet.
	if amount.Currency != w.Currency {
		v.Add("currency", "must match the wallet's currency, "+w.Currency)
	}
	if err := v.Err(); err != nil {
		return Transaction{}, err
	}

	settlement, err := s.store.SystemAccount(ctx, SettlementAccount, w.Currency)
	if err != nil {
		// %v, not %w: a missing settlement account is our bug, so the client
		// must get a 500, not a 404.
		return Transaction{}, fmt.Errorf("settlement account for %s: %v", w.Currency, err)
	}
	return s.store.Deposit(ctx, settlement, w, amount.Amount)
}

// checkAmount records a problem in v if amount can't be moved: money moves in
// positive amounts, up to MaxAmount at a time.
func checkAmount(v *apperr.ValidationError, amount int64) {
	switch {
	case amount <= 0:
		v.Add("amount", "must be a positive number of minor units, like 1050 for 10.50")
	case amount > MaxAmount:
		v.Add("amount", fmt.Sprintf("must be at most %d", MaxAmount))
	}
}

// A Transfer is money that moved from one wallet to another.
type Transfer struct {
	TransactionID uuid.UUID
	From, To      uuid.UUID
	Amount        Money
	CreatedAt     time.Time
}

// Transfer moves money between two wallets in the same currency, as one
// balanced transaction: a debit to the sender, a credit to the receiver.
func (s *Service) Transfer(ctx context.Context, fromID, toID string, amount Money) (Transfer, error) {
	var v apperr.ValidationError
	checkAmount(&v, amount.Amount)
	from, err := s.walletField(ctx, &v, "from", fromID)
	if err != nil {
		return Transfer{}, err
	}
	// Money can go to anyone's wallet: look the receiver up without a user.
	to, err := (&Service{store: s.store}).walletField(ctx, &v, "to", toID)
	if err != nil {
		return Transfer{}, err
	}

	// Compare the wallets only when both exist.
	if from.ID != uuid.Nil && to.ID != uuid.Nil {
		switch {
		case from.ID == to.ID:
			v.Add("to", "must be a different wallet")
		case to.Currency != from.Currency:
			v.Add("to", "must be a "+from.Currency+" wallet, like from")
		case amount.Currency != from.Currency:
			v.Add("currency", "must match the wallets' currency, "+from.Currency)
		}
	}
	if err := v.Err(); err != nil {
		return Transfer{}, err
	}

	tx, err := s.store.Transfer(ctx, from, to, amount.Amount)
	if err != nil {
		return Transfer{}, err
	}
	return Transfer{
		TransactionID: tx.ID,
		From:          from.ID,
		To:            to.ID,
		Amount:        amount,
		CreatedAt:     tx.CreatedAt,
	}, nil
}

// walletField loads the wallet that a request field names. A malformed or
// unknown ID is a problem with that field, recorded in v: only unexpected
// errors are returned.
func (s *Service) walletField(ctx context.Context, v *apperr.ValidationError, field, id string) (Account, error) {
	wid, err := uuid.Parse(id)
	if err != nil {
		v.Add(field, "must be a wallet ID")
		return Account{}, nil
	}
	w, err := s.Wallet(ctx, wid)
	if errors.Is(err, ErrWalletNotFound) {
		v.Add(field, "no such wallet")
		return Account{}, nil
	}
	return w, err
}

// Page sizes for listing entries.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// EntryPage is one page of a wallet's entries, newest first. NextCursor
// fetches the page after it, and is empty on the last page.
type EntryPage struct {
	Entries    []Entry
	NextCursor string
}

// Entries returns a page of a wallet's entries. An empty cursor starts at the
// newest entry.
func (s *Service) Entries(ctx context.Context, walletID uuid.UUID, cursor string, limit int) (EntryPage, error) {
	var v apperr.ValidationError
	if limit < 1 || limit > MaxPageSize {
		v.Add("limit", fmt.Sprintf("must be between 1 and %d", MaxPageSize))
	}
	before, err := decodeCursor(cursor)
	if err != nil {
		v.Add("cursor", "invalid cursor")
	}
	if err := v.Err(); err != nil {
		return EntryPage{}, err
	}
	if _, err := s.Wallet(ctx, walletID); err != nil {
		return EntryPage{}, err
	}

	// Fetch one entry more than the page holds. If it comes back, there's
	// another page, and we know without counting anything.
	entries, err := s.store.Entries(ctx, walletID, before, int32(limit+1))
	if err != nil {
		return EntryPage{}, err
	}
	page := EntryPage{Entries: entries}
	if len(entries) > limit {
		page.Entries = entries[:limit]
		page.NextCursor = encodeCursor(entries[limit-1].ID)
	}
	return page, nil
}

// A cursor is the ID of the last entry on the previous page, encoded so it's
// opaque: clients pass it back but never build one, so we can change what's
// inside without breaking them.
func encodeCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

func decodeCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil // the first page
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(string(b), 10, 64)
}
