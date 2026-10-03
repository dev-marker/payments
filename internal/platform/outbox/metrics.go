package outbox

import (
	"context"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// RegisterMetrics reports the outbox's backlog to Prometheus, fresh at every
// scrape: how many events wait to be published, and how long the oldest has
// waited. A growing lag means the relay is stuck, or NATS is down.
func RegisterMetrics(pool *pgxpool.Pool) {
	query := func(sql string) func() float64 {
		return func() float64 {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var v float64
			if err := pool.QueryRow(ctx, sql).Scan(&v); err != nil {
				return math.NaN() // unknown, which isn't zero
			}
			return v
		}
	}
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "outbox_pending_events",
		Help: "Events written to the outbox, and not published yet.",
	}, query(`SELECT count(*) FROM outbox WHERE published_at IS NULL`))
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "outbox_lag_seconds",
		Help: "How long the oldest unpublished event has waited.",
	}, query(`SELECT coalesce(extract(epoch FROM now() - min(created_at)), 0)
		FROM outbox WHERE published_at IS NULL`))
}
