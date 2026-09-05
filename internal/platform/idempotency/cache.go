package idempotency

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// cacheTTL is how long Redis keeps a completed response. Most retries come
// within minutes. Older ones still work: they're answered by Postgres.
const cacheTTL = time.Hour

// cachedResponse is a completed response as Redis holds it. It keeps the
// fingerprint, so a reused key is refused on the fast path too.
type cachedResponse struct {
	Fingerprint []byte `json:"fingerprint"`
	Status      int    `json:"status"`
	Body        []byte `json:"body"`
}

func cacheKey(k Key) string {
	return "idempotency:" + k.Scope + ":" + k.Value
}

// cached looks k up in Redis, and returns the response to replay. When Redis
// doesn't know k, or is down, it returns nothing, and Postgres decides.
func (s *Store) cached(ctx context.Context, k Key) (Response, error) {
	if s.cache == nil {
		return Response{}, nil
	}
	data, err := s.cache.Get(ctx, cacheKey(k)).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			slog.WarnContext(ctx, "idempotency cache unavailable, using postgres", "err", err)
		}
		return Response{}, nil
	}
	var c cachedResponse
	if err := json.Unmarshal(data, &c); err != nil {
		slog.WarnContext(ctx, "idempotency cache: bad entry", "key", cacheKey(k), "err", err)
		return Response{}, nil
	}
	if !bytes.Equal(c.Fingerprint, k.Fingerprint) {
		return Response{}, ErrKeyReused
	}
	return Response{Status: c.Status, Body: c.Body, Replayed: true}, nil
}

// remember stores a completed response in Redis. If that fails, we only lose
// the fast path, so the error is logged, never returned.
func (s *Store) remember(ctx context.Context, k Key, resp Response) {
	if s.cache == nil {
		return
	}
	data, err := json.Marshal(cachedResponse{Fingerprint: k.Fingerprint, Status: resp.Status, Body: resp.Body})
	if err == nil {
		err = s.cache.Set(ctx, cacheKey(k), data, cacheTTL).Err()
	}
	if err != nil {
		slog.WarnContext(ctx, "idempotency cache unavailable, response not cached", "err", err)
	}
}
