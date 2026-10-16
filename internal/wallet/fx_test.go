package wallet_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/fiston-user/payments/internal/fx"
	"github.com/fiston-user/payments/internal/wallet"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// rates are the tests' own: fixed, whatever devrates.json says.
var rates = fx.Static{"USD/EUR": 9_215_000_000, "EUR/USD": 10_852_000_000}

// sellHundred asks the price of a hundred dollars, in euros.
var sellHundred = wallet.QuoteRequest{From: "USD", To: "EUR", SellAmount: 10000}

// converter returns a Service on s that converts, for a fee of half a percent,
// and an owner with $500 in a USD wallet, and an empty EUR wallet.
func converter(t *testing.T, s *wallet.Store, ttl time.Duration) (
	svc *wallet.Service, owner, dollars, euros uuid.UUID,
) {
	t.Helper()
	svc = wallet.NewService(s).WithFX(wallet.FX{Rates: rates, QuoteTTL: ttl, FeeBps: 50})
	owner = uuid.New()
	ids := make(map[string]uuid.UUID)
	for _, currency := range []string{"USD", "EUR"} {
		w, err := svc.CreateWallet(t.Context(), owner.String(), currency)
		if err != nil {
			t.Fatalf("create %s wallet: %v", currency, err)
		}
		ids[currency] = w.ID
	}
	if _, err := svc.Deposit(t.Context(), ids["USD"], usd(50_000)); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	return svc, owner, ids["USD"], ids["EUR"]
}

func TestQuote(t *testing.T) {
	t.Parallel()
	svc, owner, _, _ := converter(t, newStore(t), time.Minute)
	tests := []struct {
		name      string
		from, to  string
		sell, buy int64
		want      string
	}{
		{"sell a hundred dollars", "USD", "EUR", 10000, 0, "10000 USD for 9215 EUR, fee 50"},
		{"buy fifty euros", "USD", "EUR", 0, 5000, "5426 USD for 5000 EUR, fee 28"},
		{"sell ten euros", "EUR", "USD", 1000, 0, "1000 EUR for 1085 USD, fee 5"},
		{"a cent buys nothing", "USD", "EUR", 1, 0, "invalid input: sell_amount"},
		{"sell and buy", "USD", "EUR", 100, 100, "invalid input: sell_amount"},
		{"no conversion", "USD", "USD", 100, 0, "invalid input: to"},
		{"unknown currency", "USD", "XXX", 100, 0, "invalid input: to"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := wallet.QuoteRequest{From: tt.from, To: tt.to, SellAmount: tt.sell, BuyAmount: tt.buy}
			q, err := svc.Quote(t.Context(), owner, req)
			got := fmt.Sprintf("%d %s for %d %s, fee %d",
				q.SellAmount, q.SellCurrency, q.BuyAmount, q.BuyCurrency, q.Fee)
			if err != nil {
				got = err.Error()
			}
			if got != tt.want {
				t.Errorf("Quote = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConvertOnce(t *testing.T) {
	t.Parallel()
	svc, owner, dollars, euros := converter(t, newStore(t), time.Minute)
	quote, err := svc.Quote(t.Context(), owner, sellHundred)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}

	// Ten requests for the same quote, at once: one converts, nine find it used.
	errs := concurrently(10, func() error {
		_, err := svc.Convert(t.Context(), owner, quote.ID)
		return err
	})
	var converted, used int
	for _, err := range errs {
		switch {
		case err == nil:
			converted++
		case errors.Is(err, wallet.ErrQuoteUsed):
			used++
		default:
			t.Errorf("convert: %v", err)
		}
	}
	if converted != 1 || used != 9 {
		t.Errorf("%d converted, %d found it used; want 1 and 9", converted, used)
	}

	// $100, and the 50-cent fee, out; €92.15 in. After the test, checkBooks
	// makes sure every currency still balances on its own.
	for id, want := range map[uuid.UUID]int64{dollars: 50_000 - 10_050, euros: 9215} {
		if b, err := svc.Balance(t.Context(), id); err != nil || b.Balance != want {
			t.Errorf("%s balance = %d, %v; want %d", b.Currency, b.Balance, err, want)
		}
	}
}

func TestConvertRefuses(t *testing.T) {
	t.Parallel()
	svc, owner, _, _ := converter(t, newStore(t), 50*time.Millisecond)
	quote, err := svc.Quote(t.Context(), owner, sellHundred)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	// Someone else's quote: to them, it doesn't exist.
	if _, err := svc.Convert(t.Context(), uuid.New(), quote.ID); !errors.Is(err, wallet.ErrQuoteNotFound) {
		t.Errorf("someone else's quote: %v, want ErrQuoteNotFound", err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := svc.Convert(t.Context(), owner, quote.ID); !errors.Is(err, wallet.ErrQuoteExpired) {
		t.Errorf("an expired quote: %v, want ErrQuoteExpired", err)
	}
}

// A conversion in two entries, dollars out and euros in, balances in no
// currency at all. Our trigger from episode 2 refuses it.
func TestTwoEntryConversionIsUnbalanced(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	_, _, dollars, euros := converter(t, s, time.Minute)
	_, err := s.PostTransaction(t.Context(), wallet.NewTransaction{
		Description: "a conversion, done wrong",
		Postings: []wallet.Posting{
			{AccountID: dollars, Direction: wallet.Debit, Amount: 10000, Currency: "USD"},
			{AccountID: euros, Direction: wallet.Credit, Amount: 9215, Currency: "EUR"},
		},
	})
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "23514" {
		t.Fatalf("got %v, want a check_violation (23514) from Postgres", err)
	}
}
