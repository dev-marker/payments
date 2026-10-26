package jobs_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/fiston-user/payments/internal/platform/jobs"
)

// TestOneLeader runs two replicas' electors on one lock: one leads, the other
// waits, and takes over when the first one's replica goes away.
func TestOneLeader(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration test")
	}
	leaders := make(chan string, 2)
	var wg sync.WaitGroup
	t.Cleanup(wg.Wait) // t.Context() is done by then: every elector stops
	elect := func(ctx context.Context, name string) {
		e := jobs.NewElector(postgresURL, t.Name(), slog.New(slog.DiscardHandler))
		e.Retry = 50 * time.Millisecond
		wg.Go(func() {
			e.Run(ctx, func(ctx context.Context) {
				leaders <- name
				<-ctx.Done() // lead until told to stop
			})
		})
	}
	// expect waits a second for the next leader: want, or nobody, for "".
	expect := func(want string) {
		t.Helper()
		select {
		case got := <-leaders:
			if got != want {
				t.Fatalf("%s leads, want %q", got, want)
			}
		case <-time.After(time.Second):
			if want != "" {
				t.Fatalf("nobody leads, want %s", want)
			}
		}
	}
	a, stopA := context.WithCancel(t.Context())
	elect(a, "a")
	expect("a")
	elect(t.Context(), "b")
	expect("") // a still leads: b waits
	stopA()    // a's replica goes away
	expect("b")
}
