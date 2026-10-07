package notification

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// maxCached caps the cache's size: once it's full, it starts over.
const maxCached = 10_000

// CachedUsers remembers addresses for a while. A transfer sends two emails,
// and an active user gets many, so most lookups never reach auth. A changed
// address is picked up within ttl.
type CachedUsers struct {
	users Users
	ttl   time.Duration

	mu      sync.Mutex // guards entries: Email is called from many goroutines
	entries map[uuid.UUID]cachedEmail
}

type cachedEmail struct {
	email   string
	expires time.Time
}

// NewCachedUsers returns users, with every address cached for ttl.
func NewCachedUsers(users Users, ttl time.Duration) *CachedUsers {
	return &CachedUsers{users: users, ttl: ttl, entries: make(map[uuid.UUID]cachedEmail)}
}

func (c *CachedUsers) Email(ctx context.Context, id uuid.UUID) (string, error) {
	c.mu.Lock()
	e, ok := c.entries[id]
	c.mu.Unlock()
	if ok && time.Now().Before(e.expires) {
		return e.email, nil
	}
	// Not while holding the lock: one slow call to auth would stall them all.
	email, err := c.users.Email(ctx, id)
	if err != nil {
		return "", err // never cached: the next try asks again
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxCached {
		clear(c.entries)
	}
	c.entries[id] = cachedEmail{email: email, expires: time.Now().Add(c.ttl)}
	return email, nil
}
