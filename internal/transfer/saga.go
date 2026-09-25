package transfer

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

// The saga, as data: in each state, the step to run, where success leads,
// and where a rejection leads. A release is retried until it works.
var saga = map[State]struct {
	name           string
	run            func(Wallet, context.Context, Transfer) error
	next, rejected State
}{
	Pending:      {"hold", Wallet.Hold, FundsHeld, Failed},
	FundsHeld:    {"capture", Wallet.Capture, Completed, Compensating},
	Compensating: {"release", Wallet.Release, Reversed, ""},
}

// An Orchestrator drives transfers through the saga, one step at a time.
// Any number can run: claims and versions keep them out of each other's way.
type Orchestrator struct {
	store     *Store
	wallet    Wallet
	logger    *slog.Logger
	wake      chan struct{}
	BatchSize int           // transfers per claim
	Lease     time.Duration // how long a claim lasts: longer than any step
	Poll      time.Duration
	Backoff   time.Duration // the first retry's delay
}

// NewOrchestrator returns an Orchestrator with sensible defaults.
func NewOrchestrator(store *Store, wallet Wallet, logger *slog.Logger) *Orchestrator {
	return &Orchestrator{
		store: store, wallet: wallet, logger: logger, wake: make(chan struct{}, 1),
		BatchSize: 10, Lease: 10 * time.Second, Poll: time.Second, Backoff: time.Second,
	}
}

// Wake tells Run there's a new transfer, without waiting for a poll.
func (o *Orchestrator) Wake() {
	select {
	case o.wake <- struct{}{}:
	default: // a wake-up is already waiting
	}
}

// Run claims due transfers and runs their next step, until ctx is canceled.
func (o *Orchestrator) Run(ctx context.Context) {
	o.logger.Info("orchestrator started", "lease", o.Lease)
	for ctx.Err() == nil {
		n, err := o.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			o.logger.Warn("claim transfers", "err", err)
		}
		if n > 0 {
			continue // their next steps are due right away
		}
		select {
		case <-ctx.Done():
		case <-o.wake:
		case <-time.After(o.Poll):
		}
	}
}

// RunOnce runs one step of each transfer in a claimed batch.
func (o *Orchestrator) RunOnce(ctx context.Context) (int, error) {
	batch, err := o.store.Claim(ctx, o.BatchSize, o.Lease)
	if err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	for _, t := range batch {
		// A step that has started finishes, even during shutdown.
		wg.Go(func() { o.step(context.WithoutCancel(ctx), t) })
	}
	wg.Wait()
	return len(batch), nil
}

// step runs t's next step, and moves t on, or schedules a retry.
func (o *Orchestrator) step(ctx context.Context, t Transfer) {
	s := saga[t.State]
	log := o.logger.With("transfer", t.ID, "step", s.name)
	err := s.run(o.wallet, ctx, t)
	var rejection *Rejection
	switch {
	case err == nil:
		t, err = o.store.Advance(ctx, t, s.next, Step{s.name, "succeeded", ""})
		if err == nil {
			log.Info("step succeeded", "state", t.State)
		}
	case errors.As(err, &rejection) && s.rejected != "":
		t.FailureReason = rejection.Reason
		t, err = o.store.Advance(ctx, t, s.rejected, Step{s.name, "rejected", rejection.Reason})
		if err == nil {
			log.Warn("step rejected", "reason", t.FailureReason, "state", t.State)
		}
	default:
		// Maybe next time. A timeout doesn't mean it didn't happen: the
		// retry is the same idempotent call.
		delay := o.backoff(t.attempts)
		log.Warn("step failed, retrying", "attempt", t.attempts+1, "in", delay, "err", err)
		err = o.store.Retry(ctx, t, Step{s.name, "failed", err.Error()}, time.Now().Add(delay))
	}
	if errors.Is(err, ErrStale) {
		log.Info("transfer moved on without us")
	} else if err != nil {
		log.Error("record step", "err", err) // the lease runs out: it runs again
	}
}

// backoff doubles with each attempt, up to a minute, with jitter.
func (o *Orchestrator) backoff(attempts int32) time.Duration {
	d := min(o.Backoff<<min(attempts, 16), time.Minute)
	return (d/2 + rand.N(d/2+1)).Round(time.Millisecond)
}
