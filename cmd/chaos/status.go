package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"
)

// A checker looks at the system from the inside: the wallet's and the
// transfer service's databases, and NATS.
type checker struct {
	wallet, transfer *pgxpool.Pool
	js               jetstream.JetStream
}

// A status is where things stand right now: our transfers, by state, and the
// events still waiting in an outbox.
type status struct {
	states map[string]int
	outbox int
}

// inFlight is how many of our transfers are still in their saga.
func (s status) inFlight() int {
	return s.states["pending"] + s.states["in_review"] + s.states["funds_held"] + s.states["compensating"]
}

// status asks both databases where our transfers are, and what's left in
// their outboxes.
func (c *checker) status(ctx context.Context, wallets []uuid.UUID) (status, error) {
	s := status{states: map[string]int{}}
	rows, _ := c.transfer.Query(ctx, `SELECT state, count(*) FROM transfers
		WHERE from_wallet_id = ANY($1) GROUP BY state`, wallets)
	var state string
	var n int
	_, err := pgx.ForEachRow(rows, []any{&state, &n}, func() error {
		s.states[state] = n
		return nil
	})
	if err != nil {
		return s, fmt.Errorf("transfers: %w", err)
	}
	for _, db := range []*pgxpool.Pool{c.wallet, c.transfer} {
		err := db.QueryRow(ctx, "SELECT count(*) FROM outbox WHERE published_at IS NULL").Scan(&n)
		if err != nil {
			return s, fmt.Errorf("outbox: %w", err)
		}
		s.outbox += n
	}
	return s, nil
}
