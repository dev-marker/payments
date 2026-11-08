package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// backlog counts the consumers on every stream, and the messages they haven't
// acknowledged yet.
func (c *checker) backlog(ctx context.Context) (consumers, pending int, err error) {
	streams := c.js.StreamNames(ctx)
	for name := range streams.Name() {
		stream, err := c.js.Stream(ctx, name)
		if err != nil {
			return 0, 0, fmt.Errorf("stream %s: %w", name, err)
		}
		infos := stream.ListConsumers(ctx)
		for info := range infos.Info() {
			consumers++
			pending += int(info.NumPending) + info.NumAckPending
		}
		if err := infos.Err(); err != nil {
			return 0, 0, fmt.Errorf("consumers of %s: %w", name, err)
		}
	}
	return consumers, pending, streams.Err()
}

// A verdict is one invariant: what it promises, whether it held, and the
// numbers that say so.
type verdict struct {
	ok      bool
	promise string
	detail  string
}

// A record is one of our transfers, as the transfer service has it.
type record struct {
	ID, From, To uuid.UUID
	Key          string
	Amount       int64
	State        string
}

// A hold is the wallet's side of a transfer: the money it set aside.
type hold struct {
	status string
	to     *uuid.UUID
	amount int64
}

// verdicts checks, once everything has settled, that the API, the sagas and
// the ledger all tell the same story about every cent.
func (c *checker) verdicts(ctx context.Context, l *load, deposit int64) ([]verdict, error) {
	rows, _ := c.transfer.Query(ctx, `SELECT id, from_wallet_id, to_wallet_id, idempotency_key,
		amount, state FROM transfers WHERE from_wallet_id = ANY($1)`, l.wallets())
	records, err := pgx.CollectRows(rows, pgx.RowToStructByPos[record])
	if err != nil {
		return nil, fmt.Errorf("transfers: %w", err)
	}
	ids := make([]uuid.UUID, len(records))
	for i, r := range records {
		ids[i] = r.ID
	}
	holds := map[uuid.UUID]hold{}
	rows, _ = c.wallet.Query(ctx, `SELECT id, status, settled_to, amount FROM holds
		WHERE id = ANY($1)`, ids)
	var id uuid.UUID
	var h hold
	_, err = pgx.ForEachRow(rows, []any{&id, &h.status, &h.to, &h.amount}, func() error {
		holds[id] = h
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("holds: %w", err)
	}
	balances := map[uuid.UUID]int64{}
	rows, _ = c.wallet.Query(ctx, `SELECT account_id, balance FROM account_balances
		WHERE account_id = ANY($1)`, l.wallets())
	var balance int64
	_, err = pgx.ForEachRow(rows, []any{&id, &balance}, func() error {
		balances[id] = balance
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("balances: %w", err)
	}
	return []verdict{
		requests(l.sent, records),
		sagas(records),
		ledger(records, holds),
		money(l, records, balances, deposit),
	}, nil
}

// requests: every request made exactly one transfer, the one the API told
// us about, however many times we had to send it.
func requests(sent []sent, records []record) verdict {
	byKey := map[string]uuid.UUID{}
	for _, r := range records {
		byKey[r.Key] = r.ID
	}
	var answered, replayed int
	for _, s := range sent {
		if s.id != uuid.Nil && byKey[s.key] == s.id {
			answered++
		}
		if s.replayed {
			replayed++
		}
	}
	return verdict{
		ok:      answered == len(sent) && len(records) == len(sent),
		promise: "every request made one transfer",
		detail: fmt.Sprintf("%s sent · %s transfers · %d replayed",
			thousands(len(sent)), thousands(len(records)), replayed),
	}
}

// sagas: every saga has ended. Nothing is left half done.
func sagas(records []record) verdict {
	n := map[string]int{}
	for _, r := range records {
		n[r.State]++
	}
	return verdict{
		ok:      n["completed"]+n["failed"]+n["reversed"] == len(records),
		promise: "no transfer stuck",
		detail: fmt.Sprintf("%s completed · %d failed · %d reversed",
			thousands(n["completed"]), n["failed"], n["reversed"]),
	}
}

// ledger: the wallet agrees with every saga. A completed transfer's hold was
// captured, into its receiver's wallet, a reversed one's went back to its
// sender, and a failed transfer never held anything.
func ledger(records []record, holds map[uuid.UUID]hold) verdict {
	var captured, released, never, wrong int
	for _, r := range records {
		h, held := holds[r.ID]
		switch {
		case !held:
			if r.State != "failed" {
				wrong++
				continue
			}
			never++
		case h.amount != r.Amount:
			wrong++
		case r.State == "completed" && h.status == "captured" && *h.to == r.To:
			captured++
		case r.State == "reversed" && h.status == "released" && *h.to == r.From:
			released++
		default:
			wrong++
		}
	}
	return verdict{
		ok:      wrong == 0,
		promise: "the ledger agrees with every saga",
		detail: fmt.Sprintf("%s captured · %d released · %d never held · %d wrong",
			thousands(captured), released, never, wrong),
	}
}

// money: every wallet holds exactly what its deposit and its completed
// transfers say, so the total is what we put in. Not a cent was lost, made
// up, or moved twice.
func money(l *load, records []record, balances map[uuid.UUID]int64, deposit int64) verdict {
	want := map[uuid.UUID]int64{}
	for _, c := range l.customers {
		want[c.wallet] = deposit
	}
	for _, r := range records {
		if r.State == "completed" {
			want[r.From] -= r.Amount
			want[r.To] += r.Amount
		}
	}
	in := deposit * int64(len(l.customers))
	var now int64
	var off int
	for _, w := range l.wallets() {
		now += balances[w]
		if balances[w] != want[w] {
			off++
		}
	}
	return verdict{
		ok:      off == 0 && now == in,
		promise: "every balance matches its transfers",
		detail: fmt.Sprintf("%d wallets · %d off · %s in · %s now",
			len(l.wallets()), off, dollars(in), dollars(now)),
	}
}

// delivered: every event left its outbox, and every consumer has it.
func delivered(outbox, consumers, pending int) verdict {
	return verdict{
		ok:      outbox == 0 && pending == 0,
		promise: "every event delivered",
		detail:  fmt.Sprintf("%d in outboxes · %d consumers · %d pending", outbox, consumers, pending),
	}
}

// reconciled waits for the wallet's reconciliation job to run once more, from
// the start, and returns what it found.
func (c *checker) reconciled(ctx context.Context) (verdict, error) {
	// It runs every fifteen seconds in Compose: a minute without a run is an error.
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var since time.Time
	if err := c.wallet.QueryRow(ctx, "SELECT now()").Scan(&since); err != nil {
		return verdict{}, fmt.Errorf("reconciliation: %w", err)
	}
	for {
		var checks, found int
		err := c.wallet.QueryRow(ctx, `SELECT (summary->>'checks')::int,
			jsonb_array_length(summary->'discrepancies') FROM job_runs
			WHERE job = 'reconcile' AND status = 'succeeded' AND started_at > $1
			ORDER BY id DESC LIMIT 1`, since).Scan(&checks, &found)
		if err == nil {
			return verdict{
				ok:      found == 0,
				promise: "the reconciliation finds nothing",
				detail:  fmt.Sprintf("%d checks · %d discrepancies", checks, found),
			}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return verdict{}, fmt.Errorf("reconciliation: %w", err)
		}
		select {
		case <-ctx.Done():
			return verdict{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
