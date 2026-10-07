package gateway_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fiston-user/payments/internal/gateway"
	"github.com/fiston-user/payments/internal/platform/redis"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// testRedis connects to the package's Redis.
func testRedis(t *testing.T) *goredis.Client {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	rdb, err := redis.NewClient(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rdb.Close() })
	return rdb
}

func TestLimiter(t *testing.T) {
	t.Parallel()
	l := gateway.NewLimiter(testRedis(t), 3, time.Minute)
	key := uuid.NewString() // a bucket of our own

	// A full bucket: three requests at once, then a refusal. A token comes
	// back every twenty seconds.
	for left := 2; left >= 0; left-- {
		d, err := l.Allow(t.Context(), key)
		if err != nil || !d.Allowed || d.Remaining != left {
			t.Fatalf("Allow = %+v, %v; want allowed with %d left", d, err, left)
		}
	}
	d, err := l.Allow(t.Context(), key)
	if err != nil || d.Allowed || d.RetryAfter < 19*time.Second || d.RetryAfter > 20*time.Second {
		t.Errorf("Allow = %+v, %v; want refused, retry after about 20s", d, err)
	}
	// Every client has a bucket of its own.
	if d, err := l.Allow(t.Context(), key+"-other"); err != nil || !d.Allowed {
		t.Errorf("another key: Allow = %+v, %v; want allowed", d, err)
	}
}

func TestLimiterIsAtomic(t *testing.T) {
	t.Parallel()
	l := gateway.NewLimiter(testRedis(t), 10, time.Hour)
	key := uuid.NewString()

	// Fifty requests at once, like fifty gateways: exactly ten get a token.
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if d, err := l.Allow(t.Context(), key); err == nil && d.Allowed {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	if n := allowed.Load(); n != 10 {
		t.Errorf("%d of 50 requests allowed, want 10", n)
	}
}
