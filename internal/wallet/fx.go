package wallet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fiston-user/payments/internal/fx"
	"github.com/fiston-user/payments/internal/platform/apperr"
	"github.com/fiston-user/payments/internal/wallet/walletdb"
	"github.com/fiston-user/payments/internal/wallet/walletevents"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The house's system accounts for conversions, one of each per currency: our
// position in the currency, and the fees we've earned in it.
const (
	FXAccount     = "fx"
	FXFeesAccount = "fx_fees"
)

type FXQuote = walletdb.FXQuote

var (
	ErrQuoteNotFound = fmt.Errorf("quote %w", apperr.ErrNotFound)
	ErrQuoteExpired  = apperr.NewRule("quote_expired", "quote expired: ask for a new one")
	ErrQuoteUsed     = apperr.NewRule("quote_used", "quote was already used")
	ErrNoWallet      = apperr.NewRule("wallet_required", "open a wallet in that currency first")
)

// FX is how a Service converts: where rates come from, how long a quote's
// price holds, and our fee, in basis points of what the customer sells.
type FX struct {
	Rates    fx.Provider
	QuoteTTL time.Duration
	FeeBps   int64
}

// WithFX returns a Service that converts between currencies, as cfg says.
func (s *Service) WithFX(cfg FX) *Service {
	c := *s
	c.fx = cfg
	return &c
}

// A QuoteRequest asks the price of selling From for To: either how much of
// From to sell, or how much of To to buy.
type QuoteRequest struct {
	From       string `json:"from"`
	To         string `json:"to"`
	SellAmount int64  `json:"sell_amount"`
	BuyAmount  int64  `json:"buy_amount"`
}

// Quote prices a conversion for owner, at the provider's rate plus our fee,
// and keeps the price for QuoteTTL: the owner can take it, once.
func (s *Service) Quote(ctx context.Context, owner uuid.UUID, req QuoteRequest) (FXQuote, error) {
	var v apperr.ValidationError
	for field, currency := range map[string]string{"from": req.From, "to": req.To} {
		_, err := s.store.SystemAccount(ctx, FXAccount, currency)
		if errors.Is(err, ErrAccountNotFound) {
			v.Add(field, "unsupported currency")
		} else if err != nil {
			return FXQuote{}, err
		}
	}
	if req.From == req.To {
		v.Add("to", "must be a different currency")
	}
	sell, buy := req.SellAmount, req.BuyAmount
	if (sell == 0) == (buy == 0) {
		v.Add("sell_amount", "set either sell_amount or buy_amount")
	}
	for field, n := range map[string]int64{"sell_amount": sell, "buy_amount": buy} {
		if n < 0 || n > MaxAmount {
			v.Add(field, fmt.Sprintf("must be between 1 and %d", MaxAmount))
		}
	}
	if err := v.Err(); err != nil {
		return FXQuote{}, err
	}

	rate, err := s.fx.Rates.Rate(ctx, req.From, req.To)
	if err != nil {
		return FXQuote{}, fmt.Errorf("rate for %s/%s: %w", req.From, req.To, err)
	}
	// What the customer gets rounds down, and what they pay rounds up: the
	// fraction of a cent in between is never ours to give away.
	if sell > 0 {
		buy, err = fx.Convert(sell, req.From, req.To, rate, fx.Down)
	} else {
		sell, err = fx.Cost(buy, req.From, req.To, rate)
	}
	if err != nil {
		return FXQuote{}, err
	}
	switch {
	case buy == 0:
		v.Add("sell_amount", "too small to buy anything")
	case sell > MaxAmount:
		v.Add("buy_amount", fmt.Sprintf("would cost more than %d", MaxAmount))
	}
	if err := v.Err(); err != nil {
		return FXQuote{}, err
	}
	return s.store.CreateQuote(ctx, walletdb.CreateQuoteParams{
		ID: uuid.Must(uuid.NewV7()), OwnerID: owner,
		SellCurrency: req.From, SellAmount: sell, BuyCurrency: req.To, BuyAmount: buy,
		Fee: fx.Fee(sell, s.fx.FeeBps), Rate: rate, TtlSeconds: s.fx.QuoteTTL.Seconds(),
	})
}

// Convert takes quote id, for owner: it moves exactly what the quote says,
// between the owner's wallets in its two currencies, or nothing at all.
func (s *Service) Convert(ctx context.Context, owner, id uuid.UUID) (FXQuote, error) {
	return s.store.Convert(ctx, owner, id)
}

// CreateQuote stores a quote. It expires TtlSeconds from now, by the
// database's clock.
func (s *Store) CreateQuote(ctx context.Context, p walletdb.CreateQuoteParams) (FXQuote, error) {
	quote, err := s.q.CreateQuote(ctx, p)
	if err != nil {
		return FXQuote{}, fmt.Errorf("create quote: %w", err)
	}
	return quote, nil
}

// Convert posts quote id as one transaction, and marks it used in the same
// one: however many requests race for a quote, it converts once. It returns
// the quote, with the transaction that used it.
func (s *Store) Convert(ctx context.Context, owner, id uuid.UUID) (FXQuote, error) {
	var used FXQuote
	err := s.inTx(ctx, func(q *walletdb.Queries) error {
		quote, err := q.LockQuote(ctx, walletdb.LockQuoteParams{ID: id, OwnerID: owner})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return ErrQuoteNotFound
		case err != nil:
			return fmt.Errorf("lock quote %s: %w", id, err)
		case quote.TransactionID != nil:
			return fmt.Errorf("quote %s: %w", id, ErrQuoteUsed)
		case quote.Expired:
			return fmt.Errorf("quote %s: %w", id, ErrQuoteExpired)
		}

		sell, buy := quote.SellCurrency, quote.BuyCurrency
		from, err := ownWallet(ctx, q, owner, sell)
		if err != nil {
			return err
		}
		to, err := ownWallet(ctx, q, owner, buy)
		if err != nil {
			return err
		}
		if err := lockInOrder(ctx, q, from.ID, to.ID); err != nil {
			return err
		}
		b, err := q.GetAccountBalance(ctx, from.ID)
		if err != nil {
			return fmt.Errorf("get balance %s: %w", from.ID, err)
		}
		if b.Balance < quote.SellAmount+quote.Fee {
			return fmt.Errorf("wallet %s: %w", from.ID, ErrInsufficientFunds)
		}

		fxSell, err := systemAccount(ctx, q, FXAccount, sell)
		if err != nil {
			return err
		}
		fxBuy, err := systemAccount(ctx, q, FXAccount, buy)
		if err != nil {
			return err
		}
		fees, err := systemAccount(ctx, q, FXFeesAccount, sell)
		if err != nil {
			return err
		}

		// Four entries convert, and each currency balances on its own: their
		// dollars go to our dollar account, and our euro account pays them euros.
		postings := []Posting{
			{AccountID: from.ID, Direction: Debit, Amount: quote.SellAmount, Currency: sell},
			{AccountID: fxSell.ID, Direction: Credit, Amount: quote.SellAmount, Currency: sell},
			{AccountID: fxBuy.ID, Direction: Debit, Amount: quote.BuyAmount, Currency: buy},
			{AccountID: to.ID, Direction: Credit, Amount: quote.BuyAmount, Currency: buy},
		}
		// Two more pay the fee, from the customer to our revenue.
		if quote.Fee > 0 {
			postings = append(postings,
				Posting{AccountID: from.ID, Direction: Debit, Amount: quote.Fee, Currency: sell},
				Posting{AccountID: fees.ID, Direction: Credit, Amount: quote.Fee, Currency: sell},
			)
		}
		posted, err := insertTransaction(ctx, q, NewTransaction{
			Description: "fx conversion", Postings: postings,
		})
		if err != nil {
			return err
		}
		used, err = q.UseQuote(ctx, walletdb.UseQuoteParams{ID: id, TransactionID: &posted.ID})
		if err != nil {
			return fmt.Errorf("use quote %s: %w", id, err)
		}
		return enqueue(ctx, q, walletevents.ConversionCompleted, from.ID, walletevents.Conversion{
			TransactionID: posted.ID,
			QuoteID:       id,
			From:          eventWallet(from),
			To:            eventWallet(to),
			Sold:          quote.SellAmount,
			Fee:           quote.Fee,
			Bought:        quote.BuyAmount,
			Rate:          quote.Rate,
		})
	})
	if err != nil {
		return FXQuote{}, fmt.Errorf("convert: %w", err)
	}
	return used, nil
}

// ownWallet returns owner's wallet in currency cur, if they have one, and if
// it can move money.
func ownWallet(ctx context.Context, q *walletdb.Queries, owner uuid.UUID, cur string) (Account, error) {
	w, err := q.GetWalletByCurrency(ctx, walletdb.GetWalletByCurrencyParams{
		OwnerID: &owner, Currency: cur,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Account{}, fmt.Errorf("no %s wallet: %w", cur, ErrNoWallet)
	case err != nil:
		return Account{}, fmt.Errorf("get %s wallet: %w", cur, err)
	case w.Frozen:
		return Account{}, fmt.Errorf("wallet %s: %w", w.ID, ErrWalletFrozen)
	}
	return w, nil
}

// systemAccount returns one of our accounts, inside a transaction.
func systemAccount(ctx context.Context, q *walletdb.Queries, name, currency string) (Account, error) {
	acc, err := q.GetSystemAccount(ctx, walletdb.GetSystemAccountParams{Name: name, Currency: currency})
	if err != nil {
		return Account{}, fmt.Errorf("%s account %s: %w", name, currency, err)
	}
	return acc, nil
}
