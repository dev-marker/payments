package postgres

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// RegisterMetrics reports pool's statistics to Prometheus, read at every
// scrape, with a pool label: name. Waits that climb mean requests queue for
// a connection, which no query's own timing ever shows.
func RegisterMetrics(name string, pool *pgxpool.Pool) {
	labels := prometheus.Labels{"pool": name}
	stat := func(f func(*pgxpool.Stat) float64) func() float64 {
		return func() float64 { return f(pool.Stat()) }
	}
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "db_pool_max_conns", Help: "The most connections the pool may open.", ConstLabels: labels,
	}, stat(func(s *pgxpool.Stat) float64 { return float64(s.MaxConns()) }))
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "db_pool_acquired_conns", Help: "Connections in use right now.", ConstLabels: labels,
	}, stat(func(s *pgxpool.Stat) float64 { return float64(s.AcquiredConns()) }))
	promauto.NewCounterFunc(prometheus.CounterOpts{
		Name: "db_pool_acquires_total", Help: "Connections handed out.", ConstLabels: labels,
	}, stat(func(s *pgxpool.Stat) float64 { return float64(s.AcquireCount()) }))
	promauto.NewCounterFunc(prometheus.CounterOpts{
		Name: "db_pool_waits_total", Help: "Acquires that waited: every connection was busy.",
		ConstLabels: labels,
	}, stat(func(s *pgxpool.Stat) float64 { return float64(s.EmptyAcquireCount()) }))
	promauto.NewCounterFunc(prometheus.CounterOpts{
		Name: "db_pool_wait_seconds_total", Help: "Time spent waiting for a connection.",
		ConstLabels: labels,
	}, stat(func(s *pgxpool.Stat) float64 { return s.EmptyAcquireWaitTime().Seconds() }))
}
