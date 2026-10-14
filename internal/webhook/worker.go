package webhook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/fiston-user/payments/internal/webhook/signature"
	"github.com/fiston-user/payments/internal/webhook/webhookdb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Schedule is how long we wait before each retry: a day and a bit, in all, so
// a receiver that's down overnight still gets everything.
var Schedule = []time.Duration{
	5 * time.Second, 5 * time.Minute, 30 * time.Minute,
	2 * time.Hour, 5 * time.Hour, 10 * time.Hour, 10 * time.Hour,
}

var attempts = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "webhook_attempts_total",
	Help: "Webhook delivery attempts, by what happened next: succeeded, pending or failed.",
}, []string{"state"})

// batchSize is how many deliveries a worker claims, and sends, at once.
const batchSize = 20

// A Worker sends due deliveries, and writes down how each attempt went.
type Worker struct {
	q        *webhookdb.Queries
	client   *http.Client
	logger   *slog.Logger
	Schedule []time.Duration // the wait before each retry: after the last, we give up
	wake     chan struct{}
}

// NewWorker returns a Worker that sends with client.
func NewWorker(q *webhookdb.Queries, client *http.Client, logger *slog.Logger) *Worker {
	return &Worker{q: q, client: client, logger: logger, Schedule: Schedule, wake: make(chan struct{}, 1)}
}

// Wake makes the worker look for due deliveries now, instead of in a second.
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default: // a wake-up is already waiting
	}
}

// Run sends deliveries until ctx is canceled: whenever it's woken up, and
// every second, for retries that come due.
func (w *Worker) Run(ctx context.Context) {
	w.logger.Info("webhook worker started", "schedule", w.Schedule)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		n, err := w.DeliverDue(ctx)
		if err != nil && ctx.Err() == nil {
			w.logger.Warn("deliver webhooks", "err", err)
		}
		if n == batchSize {
			continue // there may be more, right now
		}
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-tick.C:
		}
	}
}

// DeliverDue claims the deliveries that are due, sends them all at once, and
// returns how many there were.
func (w *Worker) DeliverDue(ctx context.Context) (int, error) {
	due, err := w.q.ClaimDue(ctx, batchSize)
	if err != nil {
		return 0, fmt.Errorf("claim deliveries: %w", err)
	}
	// A claimed batch is sent, and written down, even while we shut down:
	// otherwise it waits for its lease to run out.
	ctx = context.WithoutCancel(ctx)
	var wg sync.WaitGroup
	for _, d := range due {
		wg.Go(func() { w.deliver(ctx, d) })
	}
	wg.Wait()
	return len(due), nil
}

// deliver makes one attempt, and records it: done, retry later, or give up.
func (w *Worker) deliver(ctx context.Context, d webhookdb.ClaimDueRow) {
	start := time.Now()
	status, err := w.send(ctx, d)
	attempt := webhookdb.RecordAttemptParams{
		ID: d.ID, State: "succeeded", LastStatus: int32(status),
		LastResponseMs: int32(time.Since(start).Milliseconds()),
	}
	log := w.logger.With("delivery", d.ID, "attempt", d.Attempts+1, "status", status)
	if err != nil {
		attempt.LastError = err.Error()
		attempt.State, attempt.RetryInSeconds = w.retry(int(d.Attempts) + 1)
		log.Warn("webhook failed", "state", attempt.State, "retry_in_s", attempt.RetryInSeconds, "err", err)
	} else {
		log.Info("webhook delivered", "ms", attempt.LastResponseMs)
	}
	attempts.WithLabelValues(attempt.State).Inc()
	if err := w.q.RecordAttempt(ctx, attempt); err != nil {
		log.Error("record attempt", "err", err) // the lease runs out, and it's sent again
	}
}

// retry says what happens after a failed attempt: another one, so many
// seconds from now, or none.
func (w *Worker) retry(attempts int) (string, float64) {
	if attempts > len(w.Schedule) {
		return "failed", 0
	}
	// Up to a tenth more, at random: when a receiver comes back, its retries
	// arrive spread out, instead of all at once.
	wait := w.Schedule[attempts-1]
	wait += rand.N(wait/10 + 1)
	return "pending", wait.Round(time.Millisecond).Seconds()
}

// send POSTs a delivery, signed. Any 2xx is a success, anything else a failure.
func (w *Worker) send(ctx context.Context, d webhookdb.ClaimDueRow) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(d.Payload))
	if err != nil {
		return 0, err
	}
	// The event's ID: the same on every retry, and every replay, so the
	// receiver can tell it has seen this one before.
	err = signature.Sign(req.Header, d.Secret, d.EventID.String(), time.Now(), d.Payload)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "payments-webhooks/1")
	resp, err := w.client.Do(req)
	if err != nil {
		// Do's errors are *url.Error: the method and the URL, which the
		// merchant knows, around what actually went wrong.
		return 0, errors.Unwrap(err)
	}
	defer resp.Body.Close()
	// Read a little of the body, so the connection can be reused, but never all of it.
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("receiver answered %s", resp.Status)
	}
	return resp.StatusCode, nil
}
