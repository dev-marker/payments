package jobs

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var runs = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "job_runs_total",
	Help: "Runs of scheduled jobs, by job, and whether they succeeded.",
}, []string{"job", "status"})

// A Job is work that runs on a schedule.
type Job struct {
	Name  string
	Every time.Duration // how often: 24 * time.Hour is once a day
	At    time.Duration // how far into each period: with a day, 2 * time.Hour is 02:00 UTC
	// Run does the work, and returns what it found, for the job's history.
	Run func(ctx context.Context) (summary any, err error)
}

// Next returns when j is next due after t: At past a multiple of Every, counted
// from midnight UTC. Every replica works out the same times, without talking.
func (j Job) Next(t time.Time) time.Time {
	return t.Add(-j.At).Truncate(j.Every).Add(j.Every + j.At)
}

// A Runner runs jobs when they're due, and keeps their history in the
// job_runs table, which every service with jobs has.
type Runner struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
	jobs   []Job
}

// NewRunner returns a Runner for jobs, with their history in pool's database.
func NewRunner(pool *pgxpool.Pool, logger *slog.Logger, jobs ...Job) *Runner {
	return &Runner{pool: pool, logger: logger, jobs: jobs}
}

// Run runs each job whenever it's due, until ctx is done. Only the leader
// calls it.
func (r *Runner) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, j := range r.jobs {
		wg.Go(func() {
			for {
				slot := j.Next(time.Now())
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Until(slot)):
					r.run(ctx, j, slot)
				}
			}
		})
	}
	wg.Wait()
}

// run runs j for slot, once. The slot is claimed first: if it's already in
// job_runs, someone else ran it, or is running it.
func (r *Runner) run(ctx context.Context, j Job, slot time.Time) {
	var id int64
	err := r.pool.QueryRow(ctx, `INSERT INTO job_runs (job, slot) VALUES ($1, $2)
		ON CONFLICT (job, slot) DO NOTHING RETURNING id`, j.Name, slot).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		r.logger.ErrorContext(ctx, "claim a job run", "job", j.Name, "err", err)
		return
	}
	// A run must finish before the next one is due, so runs never overlap.
	runCtx, cancel := context.WithDeadline(ctx, j.Next(slot))
	start := time.Now()
	summary, err := j.Run(runCtx)
	cancel()

	status, took := "succeeded", time.Since(start).Round(time.Millisecond)
	if err != nil {
		status, summary = "failed", map[string]string{"error": err.Error()}
		r.logger.ErrorContext(ctx, "job failed", "job", j.Name, "took", took, "err", err)
	} else {
		r.logger.InfoContext(ctx, "job succeeded", "job", j.Name, "took", took)
	}
	runs.WithLabelValues(j.Name, status).Inc()
	// Even when ctx is done: the history must say how the run ended.
	_, err = r.pool.Exec(context.WithoutCancel(ctx), `UPDATE job_runs
		SET status = $2, summary = $3, finished_at = now() WHERE id = $1`, id, status, summary)
	if err != nil {
		r.logger.ErrorContext(ctx, "record a job run", "job", j.Name, "err", err)
	}
}
