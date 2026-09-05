package idempotency

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const (
	// TTL is how long we remember a key. Most retries come within seconds; a
	// day also covers a phone that was offline overnight.
	TTL = 24 * time.Hour
	// lease is how long a claim lasts. A request that holds a key for longer
	// died before it could finish, or let go.
	lease = time.Minute
)

// Store keeps idempotency keys in Postgres, the source of truth, with an
// optional Redis cache of completed responses in front of it.
type Store struct {
	pool  *pgxpool.Pool
	cache *redis.Client // nil: no cache
}

// NewStore returns a Store that keeps keys in the idempotency_keys table of
// pool's database. cache may be nil.
func NewStore(pool *pgxpool.Pool, cache *redis.Client) *Store {
	return &Store{pool: pool, cache: cache}
}

// Run runs work once per key. The first request with a key runs work inside a
// database transaction, and stores its response in that same transaction, so
// both commit, or neither does. A retry with the same key and request gets
// the stored response back, and work doesn't run again.
func (s *Store) Run(ctx context.Context, k Key, work func(tx pgx.Tx) (Response, error)) (Response, error) {
	// The fast path: a retry of a request that completed recently.
	if resp, err := s.cached(ctx, k); resp.Replayed || err != nil {
		return resp, err
	}

	resp, err := s.claim(ctx, k)
	switch {
	case err != nil:
		return Response{}, err
	case resp.Replayed:
		s.remember(ctx, k, resp) // so the next retry takes the fast path
		return resp, nil
	}

	err = postgres.Retry(ctx, func() error {
		var err error
		resp, err = s.attempt(ctx, k, work)
		return err
	})
	if err != nil {
		// The transaction rolled back, so nothing happened. Let go of the key,
		// so the client can fix the request, and send it again with that key.
		s.release(ctx, k)
		return Response{}, err
	}
	s.remember(ctx, k, resp)
	return resp, nil
}

// claim marks k as in progress, so that this request can run it. If k exists,
// it returns the response to replay, or the reason this request can't run.
func (s *Store) claim(ctx context.Context, k Key) (Response, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Response{}, fmt.Errorf("claim key: %w", err)
	}
	defer tx.Rollback(ctx)

	// A new key goes in as in progress. If another request is inserting the
	// same key right now, the primary key makes us wait until it commits.
	tag, err := tx.Exec(ctx, `
		INSERT INTO idempotency_keys (scope, key, fingerprint, expires_at)
		VALUES ($1, $2, $3, now() + $4::interval)
		ON CONFLICT (scope, key) DO NOTHING`,
		k.Scope, k.Value, k.Fingerprint, TTL)
	if err != nil {
		return Response{}, fmt.Errorf("claim key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// The key exists: lock its row, and look at it.
		if resp, err := s.existing(ctx, tx, k); resp.Replayed || err != nil {
			return resp, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Response{}, fmt.Errorf("claim key: %w", err)
	}
	return Response{}, nil
}

// existing locks the row of a key that's already there, and decides. It
// returns the response to replay, or an error, or neither: then the key was
// abandoned, and this request takes it over.
func (s *Store) existing(ctx context.Context, tx pgx.Tx, k Key) (Response, error) {
	var (
		fingerprint, body []byte
		status            string
		code              *int
		abandoned         bool
	)
	err := tx.QueryRow(ctx, `
		SELECT fingerprint, status, response_code, response_body,
			locked_at < now() - $3::interval
		FROM idempotency_keys
		WHERE scope = $1 AND key = $2
		FOR UPDATE`,
		k.Scope, k.Value, lease).Scan(&fingerprint, &status, &code, &body, &abandoned)
	if errors.Is(err, pgx.ErrNoRows) {
		// A request that failed released it a moment ago. A retry can claim it.
		return Response{}, ErrInProgress
	}
	if err != nil {
		return Response{}, fmt.Errorf("get key: %w", err)
	}

	switch {
	case !bytes.Equal(fingerprint, k.Fingerprint):
		return Response{}, ErrKeyReused
	case status == "completed":
		return Response{Status: *code, Body: body, Replayed: true}, nil
	case !abandoned:
		return Response{}, ErrInProgress
	}
	// In progress for longer than any request takes: the request that claimed
	// it died, and its transaction never committed. Take it over.
	_, err = tx.Exec(ctx, `
		UPDATE idempotency_keys SET locked_at = now()
		WHERE scope = $1 AND key = $2`,
		k.Scope, k.Value)
	if err != nil {
		return Response{}, fmt.Errorf("take over key: %w", err)
	}
	return Response{}, nil
}

// attempt runs work and completes k, in one database transaction.
func (s *Store) attempt(ctx context.Context, k Key, work func(tx pgx.Tx) (Response, error)) (Response, error) {
	// READ COMMITTED, like the wallet's own transactions: the row locks that
	// work takes rely on it.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Response{}, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	resp, err := work(tx)
	if err != nil {
		return Response{}, err
	}
	// Complete the key in the same transaction as the work. The status check
	// proves the key is still ours: if another request completed it first, we
	// roll back, and our work never happened.
	tag, err := tx.Exec(ctx, `
		UPDATE idempotency_keys
		SET status = 'completed', response_code = $3, response_body = $4, completed_at = now()
		WHERE scope = $1 AND key = $2 AND status = 'in_progress'`,
		k.Scope, k.Value, resp.Status, resp.Body)
	if err != nil {
		return Response{}, fmt.Errorf("complete key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Response{}, ErrInProgress
	}
	if err := tx.Commit(ctx); err != nil {
		return Response{}, fmt.Errorf("commit: %w", err)
	}
	return resp, nil
}

// release deletes k's claim after its work failed, so the key can run again.
func (s *Store) release(ctx context.Context, k Key) {
	// Release it even if the client hung up, which cancels ctx.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := s.pool.Exec(ctx, `
		DELETE FROM idempotency_keys
		WHERE scope = $1 AND key = $2 AND status = 'in_progress'`,
		k.Scope, k.Value)
	if err != nil {
		// Not fatal: the lease runs out, and a retry takes the key over.
		slog.WarnContext(ctx, "release idempotency key", "err", err)
	}
}

// Cleanup deletes expired keys every interval, until ctx is canceled.
func (s *Store) Cleanup(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		tag, err := s.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE expires_at < now()`)
		if err != nil {
			slog.WarnContext(ctx, "delete expired idempotency keys", "err", err)
			continue
		}
		slog.InfoContext(ctx, "deleted expired idempotency keys", "count", tag.RowsAffected())
	}
}
