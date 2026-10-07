package notification_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/fiston-user/payments/internal/notification"
	"github.com/fiston-user/payments/internal/platform/events"
	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/fiston-user/payments/internal/transfer/transferevents"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// fakeSender keeps what it sends. fail holds its first answers, before it
// starts working.
type fakeSender struct {
	mu   sync.Mutex
	sent []notification.Message
	fail []error
}

func (s *fakeSender) Send(_ context.Context, m notification.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.fail) > 0 {
		err := s.fail[0]
		s.fail = s.fail[1:]
		return err
	}
	s.sent = append(s.sent, m)
	return nil
}

func (s *fakeSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

// users knows everyone, at example.com.
type users struct{}

func (users) Email(_ context.Context, id uuid.UUID) (string, error) {
	return id.String() + "@example.com", nil
}

// newNotifier gives a test its own tables, like the saga's tests: temporary
// copies, on a pool of one connection.
func newNotifier(t *testing.T, sender *fakeSender) (*notification.Notifier, *notification.Store) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	pool, err := postgres.NewPool(t.Context(), postgresURL, 1)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{"notifications", "dead_letters"} {
		sql := fmt.Sprintf(`CREATE TEMPORARY TABLE %s (LIKE public.%[1]s INCLUDING ALL)`, table)
		if _, err := pool.Exec(t.Context(), sql); err != nil {
			t.Fatalf("create %s: %v", table, err)
		}
	}
	store := notification.NewStore(pool)
	n := notification.NewNotifier(store, users{}, sender, slog.New(slog.DiscardHandler))
	n.Backoff = time.Millisecond
	return n, store
}

// sent is a completed transfer of $15, as the transfer service publishes it.
func sent(t *testing.T) events.Event {
	t.Helper()
	evt, err := events.New(transferevents.Completed, uuid.New(), transferevents.Transfer{
		ID: uuid.New(), UserID: uuid.New(), Amount: 1500, Currency: "USD", State: "completed",
	})
	if err != nil {
		t.Fatalf("event: %v", err)
	}
	return evt
}

func TestNotifyOnce(t *testing.T) {
	t.Parallel()
	// The first try fails: it must leave no trace, or the retry would skip.
	sender := &fakeSender{fail: []error{errors.New("smtp: connection refused")}}
	n, _ := newNotifier(t, sender)
	evt := sent(t)

	if err := n.Notify(t.Context(), evt); err == nil {
		t.Fatal("Notify with a broken sender: err = nil")
	}
	// Then the same event, delivered three more times.
	for range 3 {
		if err := n.Notify(t.Context(), evt); err != nil {
			t.Fatalf("Notify: %v", err)
		}
	}
	if got := sender.count(); got != 1 {
		t.Fatalf("sent %d emails, want 1", got)
	}
	if m := sender.sent[0]; m.Subject != "You sent 15.00 USD" || m.EventID != evt.ID {
		t.Errorf("sent %q about %s, want \"You sent 15.00 USD\" about %s", m.Subject, m.EventID, evt.ID)
	}
}

// consume makes a stream for this test alone, and starts n's consumer on it.
// It returns a function that publishes an event.
func consume(t *testing.T, n *notification.Notifier) func(events.Event) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	name := "TEST_" + rand.Text()
	cfg := jetstream.StreamConfig{Name: name, Subjects: []string{name}, Storage: jetstream.MemoryStorage}
	if _, err := js.CreateStream(t.Context(), cfg); err != nil {
		t.Fatalf("create stream: %v", err)
	}
	t.Cleanup(func() { js.DeleteStream(context.Background(), name) })
	cc, err := n.Consume(t.Context(), js, name, name)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	t.Cleanup(cc.Stop)
	return func(evt events.Event) {
		data, _ := json.Marshal(evt)
		if _, err := js.Publish(t.Context(), name, data); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
}

func TestConsume(t *testing.T) {
	t.Parallel()
	down := errors.New("smtp: connection refused")
	noSuchMailbox := notification.Permanent(errors.New("550 no such mailbox"))
	tests := []struct {
		name string
		fail []error
		dead int // after how many deliveries it's a dead letter: 0 for never
	}{
		{"sends", nil, 0},
		{"retries until it works", []error{down, down, down}, 0},
		{"dead letter at once when permanent", []error{noSuchMailbox}, 1},
		{"dead letter after five deliveries", []error{down, down, down, down, down}, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sender := &fakeSender{fail: tt.fail}
			n, store := newNotifier(t, sender)
			consume(t, n)(sent(t))

			var dead []notification.DeadLetter
			for range 500 {
				var err error
				if dead, err = store.DeadLetters(t.Context()); err != nil {
					t.Fatalf("dead letters: %v", err)
				}
				if sender.count() > 0 || len(dead) > 0 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if tt.dead == 0 {
				if sender.count() != 1 || len(dead) != 0 {
					t.Fatalf("sent %d, %d dead letters; want 1 sent, none dead", sender.count(), len(dead))
				}
				return
			}
			if sender.count() != 0 || len(dead) != 1 || dead[0].Deliveries != int32(tt.dead) {
				t.Fatalf("sent %d, dead letters %+v; want a dead letter after %d deliveries",
					sender.count(), dead, tt.dead)
			}
			// The mail server is fixed: a replay sends it, once.
			if r, err := n.Replay(t.Context()); r != 1 || err != nil || sender.count() != 1 {
				t.Errorf("Replay = %d, %v, and %d sent; want 1, nil, and 1 sent", r, err, sender.count())
			}
		})
	}
}
