package postgres

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// MaxAttempts is how many times Retry runs a transaction before giving up.
const MaxAttempts = 5

// Retry runs fn, which must run one whole database transaction, and runs it
// again when Postgres aborted it with a serialization failure or a deadlock.
// Both mean nothing happened, so another attempt is safe, as long as fn has no
// side effects outside the transaction. It gives up after MaxAttempts.
func Retry(ctx context.Context, fn func() error) error {
	backoff := 10 * time.Millisecond
	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil || !retryable(err) {
			return err
		}
		if attempt == MaxAttempts {
			return fmt.Errorf("gave up after %d attempts: %w", attempt, err)
		}
		// Full jitter: wait a random time up to backoff, so transactions that
		// just collided don't all come back at the same moment, and collide
		// again. The ceiling doubles after every attempt.
		select {
		case <-time.After(rand.N(backoff)):
		case <-ctx.Done():
			return err
		}
		backoff *= 2
	}
}

// retryable reports whether Postgres aborted a transaction only because it
// collided with a concurrent one.
func retryable(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	if !ok {
		return false
	}
	switch pgErr.Code {
	case "40001", "40P01": // serialization_failure, deadlock_detected
		return true
	}
	return false
}
