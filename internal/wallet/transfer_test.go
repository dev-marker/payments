package wallet_test

import (
	"errors"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/fiston-user/payments/internal/wallet"
	"github.com/google/uuid"
)

// usd is an amount of US cents.
func usd(cents int64) wallet.Money {
	return wallet.Money{Amount: cents, Currency: "USD"}
}

// newWallet opens a USD wallet for a new owner, and deposits amount into it.
func newWallet(t *testing.T, svc *wallet.Service, amount int64) uuid.UUID {
	t.Helper()
	w, err := svc.CreateWallet(t.Context(), uuid.NewString(), "USD")
	if err != nil {
		t.Fatalf("create wallet: %v", err)
	}
	if amount > 0 {
		if _, err := svc.Deposit(t.Context(), w.ID, usd(amount)); err != nil {
			t.Fatalf("deposit: %v", err)
		}
	}
	return w.ID
}

// concurrently calls fn n times from n goroutines, released at the same
// moment, and returns each call's error.
func concurrently(n int, fn func() error) []error {
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			errs[i] = fn()
		})
	}
	close(start) // the starting gun: every goroutine is waiting on it
	wg.Wait()
	return errs
}

// tally counts the transfers that went through, and the ones refused for
// insufficient funds. Any other error fails the test.
func tally(t *testing.T, errs []error) (ok, refused int) {
	t.Helper()
	var failed []error
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, wallet.ErrInsufficientFunds):
			refused++
		default:
			failed = append(failed, err)
		}
	}
	if len(failed) > 0 {
		t.Errorf("%d of %d transfers failed, the first with: %v", len(failed), len(errs), failed[0])
	}
	return ok, refused
}

func TestTransferCannotOverdraw(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	svc := wallet.NewService(s)
	alice, bob := newWallet(t, svc, 1000), newWallet(t, svc, 0)

	// Ten transfers of $5 out of a $10 wallet, all at once: two can succeed.
	errs := concurrently(10, func() error {
		_, err := svc.Transfer(t.Context(), alice.String(), bob.String(), usd(500))
		return err
	})
	if ok, _ := tally(t, errs); ok != 2 {
		t.Errorf("%d transfers succeeded, want 2", ok)
	}
	if got := balance(t, s, alice); got != 0 {
		t.Errorf("alice's balance = %d, want 0", got)
	}
}

func TestConcurrentTransfersConserveMoney(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	svc := wallet.NewService(s)

	// Four wallets with $100 each.
	const start = 10_000
	wallets := make([]uuid.UUID, 4)
	for i := range wallets {
		wallets[i] = newWallet(t, svc, start)
	}

	// 200 transfers of up to $50, between two random wallets, all at once.
	errs := concurrently(200, func() error {
		i, j := rand.IntN(4), rand.IntN(3)
		if j >= i {
			j++ // any wallet but i
		}
		amount := usd(rand.Int64N(5000) + 1)
		_, err := svc.Transfer(t.Context(), wallets[i].String(), wallets[j].String(), amount)
		return err
	})
	ok, refused := tally(t, errs)
	t.Logf("%d transfers went through, %d refused for insufficient funds", ok, refused)

	// Money moved around, but none was created or lost, and no wallet went negative.
	var total int64
	for _, w := range wallets {
		b := balance(t, s, w)
		if b < 0 {
			t.Errorf("wallet %s has a negative balance: %d", w, b)
		}
		total += b
	}
	if total != 4*start {
		t.Errorf("total = %d, want %d", total, 4*start)
	}
}
