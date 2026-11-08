package main

import (
	"context"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// A sent transfer: the key we sent it with, and what the API said.
type sent struct {
	key      string
	id       uuid.UUID // the transfer's ID, or uuid.Nil: we never got an answer
	replayed bool      // a retry found it already there
}

// A load sends transfers between customers at a steady rate, like clients
// do, whatever happens to the system meanwhile, and keeps track of each one.
type load struct {
	client    *client
	customers []customer
	elsewhere customer // a wallet in another currency: no transfer to it can complete

	mu      sync.Mutex
	sent    []sent
	started atomic.Int64
}

// run sends rate transfers a second for d, then waits for every one of them
// to get a final answer, or to give up.
func (l *load) run(ctx context.Context, rate int, d time.Duration) {
	var wg sync.WaitGroup
	defer wg.Wait()
	tick := time.NewTicker(time.Second / time.Duration(rate))
	defer tick.Stop()
	for end := time.After(d); ; {
		select {
		case <-ctx.Done():
			return
		case <-end:
			return
		case <-tick.C:
			// Each one keeps trying after the load stops, for two minutes:
			// longer than any fault we inject.
			wg.Go(func() {
				ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				l.transfer(ctx)
			})
		}
	}
}

// transfer sends between one and five dollars from a customer to another,
// or, once in fifty, to the wallet in another currency.
func (l *load) transfer(ctx context.Context) {
	l.started.Add(1)
	n := len(l.customers)
	i := rand.N(n)
	from, to := l.customers[i], l.customers[(i+1+rand.N(n-1))%n].wallet
	if rand.N(50) == 0 {
		to = l.elsewhere.wallet // held, then refused: the saga gives the money back
	}
	amount := 100 + rand.N(401)
	r := request{
		path:  "/v1/transfers",
		token: from.token,
		key:   uuid.NewString(),
		body:  map[string]any{"from": from.wallet, "to": to, "amount": amount, "currency": "USD"},
	}
	var t struct {
		ID uuid.UUID `json:"id"`
	}
	replayed, err := l.client.send(ctx, r, &t)
	s := sent{key: r.key}
	if err == nil {
		s.id, s.replayed = t.ID, replayed
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sent = append(l.sent, s)
}

// wallets returns the IDs of every wallet in the load.
func (l *load) wallets() []uuid.UUID {
	ids := []uuid.UUID{l.elsewhere.wallet}
	for _, c := range l.customers {
		ids = append(ids, c.wallet)
	}
	return ids
}
