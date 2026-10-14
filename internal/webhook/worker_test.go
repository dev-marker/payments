package webhook_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/fiston-user/payments/internal/transfer/transferevents"
	"github.com/fiston-user/payments/internal/webhook"
	"github.com/fiston-user/payments/internal/webhook/signature"
	"github.com/fiston-user/payments/internal/webhook/webhookdb"
	"github.com/google/uuid"
)

// receiver answers 503 to its first fails deliveries, and 204 after that. It
// verifies every one, and counts them.
func receiver(t *testing.T, secret string, fails int32) (*httptest.Server, *atomic.Int32) {
	var got atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := signature.Verify(r.Header, secret, body, time.Now()); err != nil {
			t.Errorf("delivery %d: %v", got.Load()+1, err)
		}
		if got.Add(1) <= fails {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestWorker(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	pool, err := postgres.NewPool(t.Context(), postgresURL, 2)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	q := webhookdb.New(pool)
	// Two retries, a millisecond apart: three attempts in all.
	worker := webhook.NewWorker(q, webhook.NewClient(false), slog.New(slog.DiscardHandler))
	worker.Schedule = []time.Duration{time.Millisecond, time.Millisecond}

	tests := []struct {
		name   string
		fails  int32  // 503s, before a 204
		state  string // after three attempts
		status int32  // the last one's
	}{
		{"fails twice, then works", 2, "succeeded", http.StatusNoContent},
		{"never works", 100, "failed", http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := signature.NewSecret()
			srv, got := receiver(t, secret, tt.fails)
			user := uuid.New()
			endpoint, err := q.CreateEndpoint(t.Context(), webhookdb.CreateEndpointParams{
				ID: uuid.New(), UserID: user, URL: srv.URL, EventTypes: webhook.EventTypes, Secret: secret,
			})
			if err != nil {
				t.Fatalf("create endpoint: %v", err)
			}
			_, err = q.FanOut(t.Context(), webhookdb.FanOutParams{UserID: user, EventID: uuid.New(),
				EventType: transferevents.Completed, Payload: []byte(`{"amount":1500}`)})
			if err != nil {
				t.Fatalf("fan out: %v", err)
			}
			// One round more than it takes: once it's over, it must stay over.
			for range 4 {
				if _, err := worker.DeliverDue(t.Context()); err != nil {
					t.Fatalf("DeliverDue: %v", err)
				}
				time.Sleep(10 * time.Millisecond) // the millisecond, and its jitter
			}
			log, err := q.ListDeliveries(t.Context(), webhookdb.ListDeliveriesParams{
				UserID: user, EndpointID: endpoint.ID,
			})
			if err != nil || len(log) != 1 {
				t.Fatalf("deliveries: %v, %v", log, err)
			}
			if d := log[0]; got.Load() != 3 || d.Attempts != 3 || d.State != tt.state || d.LastStatus != tt.status {
				t.Errorf("received %d, logged %d attempts, %s, last %d; want 3, 3, %s, last %d",
					got.Load(), d.Attempts, d.State, d.LastStatus, tt.state, tt.status)
			}
		})
	}
}
