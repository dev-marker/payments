// Command chaos is a game day in a box. It sends a steady stream of
// transfers through the gateway, the way a careful client does, while things
// break. Then it waits for the system to settle, and checks, against the
// ledger itself, that no money was lost, made up, or moved twice.
//
//	go run ./cmd/chaos -duration 3m -faults chaos/gameday.sh
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Where everything is, in docker-compose.yml: a test tool's defaults.
const (
	walletDB   = "postgres://payments:payments@localhost:5432/wallet?sslmode=disable"
	transferDB = "postgres://payments:payments@localhost:5432/transfer?sslmode=disable"
)

// deposit is what each customer starts with: $50, in cents.
const deposit = 5000

func main() {
	gateway := flag.String("gateway", "http://localhost:8080", "the API")
	customers := flag.Int("customers", 20, "how many customers send each other money")
	rate := flag.Int("rate", 20, "transfers a second")
	duration := flag.Duration("duration", time.Minute, "how long to send transfers for")
	settle := flag.Duration("settle", 3*time.Minute, "how long the system gets to settle afterwards")
	faults := flag.String("faults", "", "a script that breaks things while transfers are sent")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	g := gameDay{
		client: newClient(*gateway), customers: *customers, rate: *rate,
		duration: *duration, settle: *settle, faults: *faults,
	}
	ok, err := g.run(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chaos:", err)
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}

// A gameDay is one run: customers, the load, the faults, and the checks.
type gameDay struct {
	client    *client
	customers int
	rate      int
	duration  time.Duration
	settle    time.Duration
	faults    string
	start     time.Time
}

// run reports whether every invariant held. An error means we couldn't tell.
func (g *gameDay) run(ctx context.Context) (bool, error) {
	chk, err := connect(ctx)
	if err != nil {
		return false, err
	}
	l, err := g.signUp(ctx)
	if err != nil {
		return false, err
	}

	// The load, the faults alongside it, and every five seconds, where things
	// stand.
	g.start = time.Now()
	g.print("sending %d transfers a second, for %s", g.rate, g.duration)
	var wg sync.WaitGroup
	wg.Go(func() { l.run(ctx, g.rate, g.duration) })
	if g.faults != "" {
		wg.Go(func() { g.breakThings(ctx) })
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for running := true; running; {
		select {
		case <-tick.C:
			g.progress(ctx, chk, l)
		case <-done:
			running = false
		}
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	g.print("load done: %s transfers sent, %s retries", thousands(l.started.Load()),
		thousands(g.client.retries.Load()))

	// Then the system gets time to finish what it started, and we check.
	events, err := g.waitToSettle(ctx, chk, l)
	if err != nil {
		return false, err
	}
	verdicts, err := chk.verdicts(ctx, l, deposit)
	if err != nil {
		return false, err
	}
	g.print("waiting for the wallet's next reconciliation")
	rec, err := chk.reconciled(ctx)
	if err != nil {
		return false, err
	}
	return scoreboard(append(verdicts, events, rec)), nil
}

// connect opens pools on the wallet's and the transfer service's databases,
// and connects to NATS.
func connect(ctx context.Context) (*checker, error) {
	wallet, err := pgxpool.New(ctx, walletDB)
	if err != nil {
		return nil, err
	}
	transfer, err := pgxpool.New(ctx, transferDB)
	if err != nil {
		return nil, err
	}
	nc, err := nats.Connect(nats.DefaultURL, nats.MaxReconnects(-1))
	if err != nil {
		return nil, err
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, err
	}
	return &checker{wallet: wallet, transfer: transfer, js: js}, nil
}

// signUp creates the customers, with $50 each, and one more, whose wallet is
// in euros.
func (g *gameDay) signUp(ctx context.Context) (*load, error) {
	l := &load{client: g.client}
	run := time.Now().Unix()
	for i := range g.customers {
		c, err := g.client.signUp(ctx, fmt.Sprintf("chaos-%d-%d@example.com", run, i), "USD", deposit)
		if err != nil {
			return nil, err
		}
		l.customers = append(l.customers, c)
	}
	var err error
	l.elsewhere, err = g.client.signUp(ctx, fmt.Sprintf("chaos-%d-eur@example.com", run), "EUR", 0)
	if err != nil {
		return nil, err
	}
	fmt.Printf("%d customers with %s each, and a wallet in euros\n", g.customers, dollars(deposit))
	return l, nil
}

// breakThings runs the faults script, and prints each line it prints, when
// it prints it, in yellow.
func (g *gameDay) breakThings(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "sh", g.faults)
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		g.print("\x1b[31mfaults: %v\x1b[0m", err)
		return
	}
	lines := bufio.NewScanner(out)
	for lines.Scan() {
		g.print("\x1b[33m%s\x1b[0m", lines.Text())
	}
	if err := cmd.Wait(); err != nil {
		g.print("\x1b[31mfaults: %v\x1b[0m", err)
	}
}

// waitToSettle waits until every saga is over, and every event delivered.
func (g *gameDay) waitToSettle(ctx context.Context, chk *checker, l *load) (verdict, error) {
	deadline := time.Now().Add(g.settle)
	for i := 1; ; i++ {
		s, err := chk.status(ctx, l.wallets())
		consumers, pending := 0, 0
		if err == nil {
			consumers, pending, err = chk.backlog(ctx)
		}
		if err == nil && s.inFlight() == 0 && s.outbox == 0 && pending == 0 {
			g.print("settled")
			return delivered(s.outbox, consumers, pending), nil
		}
		if time.Now().After(deadline) {
			return verdict{}, fmt.Errorf("not settled after %s: %d in flight, %d events waiting (%v)",
				g.settle, s.inFlight(), s.outbox+pending, err)
		}
		if i%5 == 0 {
			g.progress(ctx, chk, l)
		}
		select {
		case <-ctx.Done():
			return verdict{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// progress prints what the load has sent, where our transfers are, and how
// many events wait in the outboxes.
func (g *gameDay) progress(ctx context.Context, chk *checker, l *load) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	sent := fmt.Sprintf("sent %4d  retried %4d", l.started.Load(), g.client.retries.Load())
	s, err := chk.status(ctx, l.wallets())
	if err != nil {
		msg := err.Error()
		g.print("%s │ \x1b[31m%s\x1b[0m", sent, msg[:min(len(msg), 60)])
		return
	}
	g.print("%s │ in flight %3d  completed %4d  failed %3d  reversed %2d │ outbox %3d",
		sent, s.inFlight(), s.states["completed"], s.states["failed"], s.states["reversed"], s.outbox)
}

// print prints a line, after how long the run has been going.
func (g *gameDay) print(format string, args ...any) {
	fmt.Printf("%4.0fs  %s\n", time.Since(g.start).Seconds(), fmt.Sprintf(format, args...))
}

// scoreboard prints every verdict, and reports whether they all held.
func scoreboard(verdicts []verdict) bool {
	fmt.Println()
	ok := true
	for _, v := range verdicts {
		mark := "\x1b[32m✓\x1b[0m"
		if !v.ok {
			mark, ok = "\x1b[31m✗\x1b[0m", false
		}
		fmt.Printf("  %s %-36s %s\n", mark, v.promise, v.detail)
	}
	if ok {
		fmt.Println("\n  \x1b[1;32mNo money lost, made up, or moved twice.\x1b[0m")
	} else {
		fmt.Println("\n  \x1b[1;31mSomething doesn't add up.\x1b[0m")
	}
	return ok
}

// thousands formats n like 1,234.
func thousands[N int | int64](n N) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// dollars formats cents like $1,234.56.
func dollars(cents int64) string {
	return fmt.Sprintf("$%s.%02d", thousands(cents/100), cents%100)
}
