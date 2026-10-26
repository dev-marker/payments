// Package jobs runs scheduled work, like a daily reconciliation, on exactly one
// replica of a service: the leader, elected with a Postgres advisory lock.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var leading = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "jobs_leader",
	Help: "1 while this replica leads, and runs the jobs. Summed over replicas: 1.",
})

// An Elector picks one leader among a service's replicas: the one whose
// database session holds an advisory lock. When that session ends, for any
// reason, Postgres releases the lock, and the next replica in line leads.
type Elector struct {
	dsn    string
	lock   string        // replicas that use the same name compete
	Retry  time.Duration // how often the leader checks its session
	logger *slog.Logger
}

// NewElector returns an Elector for the lock called name, in the database at dsn.
func NewElector(dsn, lock string, logger *slog.Logger) *Elector {
	return &Elector{dsn: dsn, lock: lock, Retry: 5 * time.Second, logger: logger}
}

// Run calls lead whenever this replica becomes the leader, with a context
// that's cancelled when it stops being one. It returns when ctx is done.
func (e *Elector) Run(ctx context.Context, lead func(ctx context.Context)) {
	for ctx.Err() == nil {
		if err := e.campaign(ctx, lead); err != nil && ctx.Err() == nil {
			e.logger.Warn("leader election", "lock", e.lock, "err", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(e.Retry): // after an error: don't hammer the database
		}
	}
}

// campaign waits for the lock on a connection of its own, then leads until
// ctx is done or the session is lost.
func (e *Elector) campaign(ctx context.Context, lead func(context.Context)) error {
	// Not a connection from the pool: the pool would hand our session, and
	// the lock with it, to whoever asks next.
	conn, err := pgx.Connect(ctx, e.dsn)
	if err != nil {
		return err
	}
	// Closing the connection ends the session: Postgres releases the lock.
	defer conn.Close(context.WithoutCancel(ctx))
	e.logger.Info("waiting to lead", "lock", e.lock)
	// This waits while another session holds the lock, and returns the moment it's free.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(hashtext($1))", e.lock); err != nil {
		return err
	}
	e.logger.Info("leading", "lock", e.lock)
	leading.Set(1)
	defer leading.Set(0)

	// Deferred calls run last first: stop the jobs, wait for them, and only
	// then let the lock go. Two leaders never run jobs at the same time.
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() { lead(ctx) })
	defer wg.Wait()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			e.logger.Info("stepping down", "lock", e.lock)
			return nil
		case <-time.After(e.Retry):
		}
		if err := ping(ctx, conn, e.Retry); err != nil {
			return fmt.Errorf("lost the session, and the lock: %w", err)
		}
	}
}

// ping checks that conn's session is alive, within timeout.
func ping(ctx context.Context, conn *pgx.Conn, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return conn.Ping(ctx)
}
