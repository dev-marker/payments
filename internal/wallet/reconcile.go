package wallet

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// A Discrepancy is something in the ledger that shouldn't be: what a check
// expected, and what it found, in minor units of the currency.
type Discrepancy struct {
	Check    string `json:"check"`
	Ref      string `json:"ref"` // the transaction or account that's off
	Currency string `json:"currency"`
	Expected int64  `json:"expected"`
	Actual   int64  `json:"actual"`
}

// A Reconciliation is what one run of every check found.
type Reconciliation struct {
	Checks        int           `json:"checks"`
	Discrepancies []Discrepancy `json:"discrepancies"`
}

// signed is an entry's amount, as debits minus credits.
const signed = `CASE direction WHEN 'debit' THEN amount ELSE -amount END`

// checks are the ledger's invariants. Each query returns whatever breaks
// one: a reference, the currency, what we expected, and what we found.
var checks = []struct{ name, query string }{
	// Debits equal credits in each currency, over every entry: the trial balance.
	{"trial_balance", `SELECT 'all entries', currency, 0, sum(` + signed + `)::bigint
		FROM entries GROUP BY currency HAVING sum(` + signed + `) <> 0`},
	// And in each transaction.
	{"unbalanced_transaction", `SELECT transaction_id::text, currency, 0, sum(` + signed + `)::bigint
		FROM entries GROUP BY transaction_id, currency HAVING sum(` + signed + `) <> 0`},
	// No customer's wallet is below zero.
	{"overdrawn_wallet", `SELECT b.account_id::text, b.currency, 0, b.balance
		FROM account_balances b JOIN accounts a ON a.id = b.account_id
		WHERE a.kind = 'wallet' AND b.balance < 0`},
	// The holds account has exactly the money of the open holds.
	{"holds_account", `SELECT b.account_id::text, b.currency, coalesce(h.held, 0)::bigint, b.balance
		FROM account_balances b JOIN accounts a ON a.id = b.account_id
		LEFT JOIN (SELECT currency, sum(amount) AS held FROM holds WHERE status = 'held'
			GROUP BY currency) h ON h.currency = b.currency
		WHERE a.kind = 'system' AND a.name = 'holds' AND b.balance <> coalesce(h.held, 0)`},
	// Events leave within five minutes: in seconds, the oldest one waiting.
	{"outbox_lag", `SELECT 'outbox', '', 300, extract(epoch FROM now() - min(created_at))::bigint
		FROM outbox WHERE published_at IS NULL HAVING min(created_at) < now() - interval '5 minutes'`},
}

// snapshot is a read-only transaction that sees the database as it was when
// it began: every query in it sees the same moment.
var snapshot = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}

// Reconcile runs every check in one snapshot: a transfer that commits halfway
// through can't look like a discrepancy. A Store from WithTx runs them in its
// caller's transaction instead.
func (s *Store) Reconcile(ctx context.Context) (Reconciliation, error) {
	tx := s.tx
	if tx == nil {
		var err error
		if tx, err = s.reports.BeginTx(ctx, snapshot); err != nil {
			return Reconciliation{}, fmt.Errorf("reconcile: %w", err)
		}
		defer tx.Rollback(ctx)
	}
	r := Reconciliation{Checks: len(checks), Discrepancies: []Discrepancy{}}
	for _, c := range checks {
		rows, _ := tx.Query(ctx, c.query)
		found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Discrepancy, error) {
			d := Discrepancy{Check: c.name}
			return d, row.Scan(&d.Ref, &d.Currency, &d.Expected, &d.Actual)
		})
		if err != nil {
			return Reconciliation{}, fmt.Errorf("reconcile: %s: %w", c.name, err)
		}
		r.Discrepancies = append(r.Discrepancies, found...)
	}
	return r, nil
}

// ReconcileJob is the reconciliation as a job: every discrepancy it finds is
// also logged, as an error, for whoever reads the logs first.
func ReconcileJob(s *Store, logger *slog.Logger) func(context.Context) (any, error) {
	return func(ctx context.Context) (any, error) {
		r, err := s.Reconcile(ctx)
		for _, d := range r.Discrepancies {
			logger.ErrorContext(ctx, "ledger discrepancy", "check", d.Check, "ref", d.Ref,
				"currency", d.Currency, "expected", d.Expected, "actual", d.Actual)
		}
		return r, err
	}
}

// RegisterMetrics reports what the last reconciliation found, read from the
// database at every scrape: every replica says the same, whichever one ran it.
func RegisterMetrics(pool *pgxpool.Pool) {
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "reconciliation_discrepancies",
		Help: "What the last reconciliation found. Anything but 0 needs a person.",
	}, func() float64 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var n float64
		err := pool.QueryRow(ctx, `SELECT jsonb_array_length(summary->'discrepancies')::float8
			FROM job_runs WHERE job = 'reconcile' AND status = 'succeeded'
			ORDER BY slot DESC LIMIT 1`).Scan(&n)
		if err != nil {
			return math.NaN() // unknown, which isn't zero
		}
		return n
	})
}
