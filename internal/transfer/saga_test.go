package transfer_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/fiston-user/payments/internal/transfer"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeWallet is an idempotent wallet in memory. fail holds the first answers
// to each step, before it says yes.
type fakeWallet struct {
	mu    sync.Mutex
	holds map[uuid.UUID]string // held, captured or released
	calls map[string]int
	fail  map[string][]error
}

func (w *fakeWallet) step(name string, id uuid.UUID, from, to string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls[name]++
	if errs := w.fail[name]; len(errs) > 0 {
		w.fail[name] = errs[1:]
		return errs[0]
	}
	switch w.holds[id] {
	case to:
		return nil // done before
	case from:
		w.holds[id] = to
		return nil
	}
	return fmt.Errorf("hold %s is %q, it can't become %s", id, w.holds[id], to)
}

func (w *fakeWallet) Hold(_ context.Context, t transfer.Transfer) error {
	return w.step("hold", t.ID, "", "held")
}

func (w *fakeWallet) Capture(_ context.Context, t transfer.Transfer) error {
	return w.step("capture", t.ID, "held", "captured")
}

func (w *fakeWallet) Release(_ context.Context, t transfer.Transfer) error {
	return w.step("release", t.ID, "held", "released")
}

type sagaTest struct {
	orch   *transfer.Orchestrator
	store  *transfer.Store
	wallet *fakeWallet
}

// newSaga gives a test its own tables, like the relay's test: temporary
// copies, on one connection. Tests must never claim each other's transfers.
func newSaga(t *testing.T) sagaTest {
	t.Helper()
	dsn := os.Getenv("TRANSFER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TRANSFER_TEST_DATABASE_URL not set, skipping integration test")
	}
	pool, err := postgres.NewPool(t.Context(), dsn, 1)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{"transfers", "transfer_steps", "outbox"} {
		sql := fmt.Sprintf(`CREATE TEMPORARY TABLE %s (LIKE public.%[1]s INCLUDING ALL)`, table)
		if _, err := pool.Exec(t.Context(), sql); err != nil {
			t.Fatalf("create %s: %v", table, err)
		}
	}
	store := transfer.NewStore(pool)
	wallet := &fakeWallet{holds: map[uuid.UUID]string{}, calls: map[string]int{}}
	orch := transfer.NewOrchestrator(store, wallet, slog.New(slog.DiscardHandler))
	orch.Backoff = time.Millisecond
	return sagaTest{orch, store, wallet}
}

// create stores a pending transfer of $15.
func (s sagaTest) create(t *testing.T) transfer.Transfer {
	t.Helper()
	tr, _, err := s.store.Create(t.Context(), transfer.NewTransfer{
		UserID:         uuid.New(),
		IdempotencyKey: uuid.NewString(),
		FromWalletID:   uuid.New(),
		ToWalletID:     uuid.New(),
		Amount:         1500,
		Currency:       "USD",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return tr
}

// finish runs the orchestrator until the saga is over.
func (s sagaTest) finish(t *testing.T, id uuid.UUID) transfer.Transfer {
	t.Helper()
	for range 100 {
		if _, err := s.orch.RunOnce(t.Context()); err != nil {
			t.Fatalf("run: %v", err)
		}
		tr, err := s.store.Get(t.Context(), id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if tr.State.Final() {
			return tr
		}
		time.Sleep(5 * time.Millisecond) // for retries
	}
	t.Fatalf("transfer %s never finished", id)
	return transfer.Transfer{}
}

func TestSaga(t *testing.T) {
	t.Parallel()
	rejected := func(reason string) error { return &transfer.Rejection{Reason: reason} }
	down := status.Error(codes.Unavailable, "wallet unavailable")
	tests := []struct {
		name   string
		fail   map[string][]error
		state  transfer.State
		reason string
		hold   string // as the wallet sees it, in the end
	}{
		{"completes", nil, transfer.Completed, "", "captured"},
		{"insufficient funds", map[string][]error{"hold": {rejected("insufficient_funds")}},
			transfer.Failed, "insufficient_funds", ""},
		{"receiver frozen", map[string][]error{"capture": {rejected("wallet_frozen")}},
			transfer.Reversed, "wallet_frozen", "released"},
		{"wallet down for a while", map[string][]error{"hold": {down, down}, "capture": {down}},
			transfer.Completed, "", "captured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newSaga(t)
			s.wallet.fail = tt.fail
			tr := s.finish(t, s.create(t).ID)

			if tr.State != tt.state || tr.FailureReason != tt.reason {
				t.Errorf("transfer is %s %q, want %s %q", tr.State, tr.FailureReason, tt.state, tt.reason)
			}
			if got := s.wallet.holds[tr.ID]; got != tt.hold {
				t.Errorf("hold is %q, want %q", got, tt.hold)
			}
		})
	}
}

func TestSagaResumesAfterCrash(t *testing.T) {
	t.Parallel()
	s := newSaga(t)
	tr := s.create(t)

	// A worker claims it, places the hold, and dies before writing it down.
	claimed, err := s.store.Claim(t.Context(), 1, time.Millisecond)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v; want the transfer", claimed, err)
	}
	if err := s.wallet.Hold(t.Context(), claimed[0]); err != nil {
		t.Fatalf("hold: %v", err)
	}
	time.Sleep(10 * time.Millisecond) // its lease runs out

	// Another worker takes over: the hold is asked for twice, placed once.
	if got := s.finish(t, tr.ID); got.State != transfer.Completed {
		t.Fatalf("transfer is %s, want completed", got.State)
	}
	if n, hold := s.wallet.calls["hold"], s.wallet.holds[tr.ID]; n != 2 || hold != "captured" {
		t.Errorf("hold called %d times, and %s; want 2 times, and captured", n, hold)
	}

	// The first worker comes back to life: its version is stale.
	_, err = s.store.Advance(t.Context(), claimed[0], transfer.FundsHeld, transfer.Step{Name: "hold"})
	if !errors.Is(err, transfer.ErrStale) {
		t.Errorf("advance by a stale worker: err = %v, want ErrStale", err)
	}
}
