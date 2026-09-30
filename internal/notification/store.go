package notification

import (
	"context"
	"fmt"

	"github.com/fiston-user/payments/internal/notification/notificationdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store remembers what we sent, and what we gave up on.
type Store struct {
	pool *pgxpool.Pool
	q    *notificationdb.Queries
}

// NewStore returns a Store on pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: notificationdb.New(pool)}
}

// Deliver records m, and calls send, in one database transaction: if send
// fails, the record rolls back. If m's event was handled before, it sends
// nothing, and returns false.
func (s *Store) Deliver(ctx context.Context, m Message, send func() error) (bool, error) {
	sent := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := s.q.WithTx(tx).InsertNotification(ctx, notificationdb.InsertNotificationParams{
			EventID:   m.EventID,
			UserID:    m.UserID,
			Recipient: m.To,
			Subject:   m.Subject,
		})
		if err != nil || n == 0 {
			return err
		}
		// Until we commit, the new row is locked: the same event, delivered
		// twice at once, waits here, and then finds it.
		sent = true
		return send()
	})
	if err != nil {
		return false, fmt.Errorf("deliver: %w", err)
	}
	return sent, nil
}

// A DeadLetter is a message we gave up on.
type DeadLetter = notificationdb.DeadLetter

// DeadLetter keeps a message we gave up on, and why.
func (s *Store) DeadLetter(ctx context.Context, d notificationdb.InsertDeadLetterParams) error {
	if err := s.q.InsertDeadLetter(ctx, d); err != nil {
		return fmt.Errorf("dead letter %s %d: %w", d.Stream, d.Seq, err)
	}
	return nil
}

// DeadLetters returns the dead letters nobody has replayed yet, oldest first.
func (s *Store) DeadLetters(ctx context.Context) ([]DeadLetter, error) {
	return s.q.ListDeadLetters(ctx)
}

// Replayed marks a dead letter as handled after all.
func (s *Store) Replayed(ctx context.Context, d DeadLetter) error {
	return s.q.MarkReplayed(ctx, notificationdb.MarkReplayedParams{Stream: d.Stream, Seq: d.Seq})
}
