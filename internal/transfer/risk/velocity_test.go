package risk_test

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/fiston-user/payments/internal/platform/redis"
	"github.com/fiston-user/payments/internal/transfer/risk"
	"github.com/google/uuid"
)

// newVelocity counts on the package's Redis.
func newVelocity(t *testing.T, count, amount time.Duration) *risk.Velocity {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	rdb, err := redis.NewClient(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rdb.Close() })
	return risk.NewVelocity(rdb, count, amount)
}

func TestVelocity(t *testing.T) {
	t.Parallel()
	v := newVelocity(t, 300*time.Millisecond, time.Hour)
	user, retried := uuid.New(), uuid.New()
	record := func(id uuid.UUID, amount int64, want risk.Recent) {
		t.Helper()
		if got, err := v.Record(t.Context(), user, id, amount); err != nil || got != want {
			t.Fatalf("Record = %+v, %v; want %+v", got, err, want)
		}
	}
	record(uuid.New(), 1500, risk.Recent{Count: 1, Amount: 1500})
	record(retried, 2500, risk.Recent{Count: 2, Amount: 4000})
	record(retried, 2500, risk.Recent{Count: 2, Amount: 4000}) // a retry counts once

	// Past the count window, inside the amount window.
	time.Sleep(400 * time.Millisecond)
	record(uuid.New(), 1000, risk.Recent{Count: 1, Amount: 5000})

	// Someone else starts from zero.
	if got, err := v.Record(t.Context(), uuid.New(), uuid.New(), 700); err != nil || got.Count != 1 {
		t.Errorf("another user: Record = %+v, %v; want a count of 1", got, err)
	}
}

func TestVelocityIsAtomic(t *testing.T) {
	t.Parallel()
	v := newVelocity(t, time.Hour, time.Hour)
	user := uuid.New()

	// Fifty transfers at once, from fifty instances: each sees a count of its
	// own, from 1 to 50. With a read, then a write, some would see the same.
	counts := make([]int, 50)
	var wg sync.WaitGroup
	for i := range counts {
		wg.Go(func() {
			r, err := v.Record(t.Context(), user, uuid.New(), 100)
			if err != nil {
				t.Error(err)
			}
			counts[i] = r.Count
		})
	}
	wg.Wait()
	slices.Sort(counts)
	for i, n := range counts {
		if n != i+1 {
			t.Fatalf("counts = %v, want 1 to 50, once each", counts)
		}
	}
}
