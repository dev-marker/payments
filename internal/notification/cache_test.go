package notification_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fiston-user/payments/internal/notification"
	"github.com/google/uuid"
)

// countingUsers counts the lookups that get through to it.
type countingUsers struct {
	calls atomic.Int32
}

func (u *countingUsers) Email(ctx context.Context, id uuid.UUID) (string, error) {
	u.calls.Add(1)
	return users{}.Email(ctx, id)
}

func TestCachedUsers(t *testing.T) {
	t.Parallel()
	auth := &countingUsers{}
	cache := notification.NewCachedUsers(auth, time.Minute)
	alice, bob := uuid.New(), uuid.New()

	// Like the consumers: lots of lookups at once, for the same two users.
	var wg sync.WaitGroup
	for i := range 100 {
		id := alice
		if i%2 == 1 {
			id = bob
		}
		wg.Go(func() {
			if got, err := cache.Email(t.Context(), id); err != nil || got != id.String()+"@example.com" {
				t.Errorf("Email = %q, %v; want %s@example.com", got, err, id)
			}
		})
	}
	wg.Wait()

	// Both are cached now: nothing else gets through to auth.
	before := auth.calls.Load()
	cache.Email(t.Context(), alice)
	cache.Email(t.Context(), bob)
	if after := auth.calls.Load(); after != before {
		t.Errorf("%d more lookups reached auth, want none", after-before)
	}
}
