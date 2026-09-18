package gateway

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/fiston-user/payments/internal/platform/authn"
	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/redis/go-redis/v9"
)

//go:embed ratelimit.lua
var tokenBucketLua string

// tokenBucket runs with EVALSHA: after the first call, only its hash is sent.
var tokenBucket = redis.NewScript(tokenBucketLua)

// A Limiter allows each client limit requests per period. Its buckets live in
// Redis, so every gateway instance shares them.
type Limiter struct {
	rdb    *redis.Client
	limit  int
	period time.Duration
}

// NewLimiter returns a Limiter that keeps its buckets in rdb.
func NewLimiter(rdb *redis.Client, limit int, period time.Duration) *Limiter {
	return &Limiter{rdb: rdb, limit: limit, period: period}
}

// A Decision is the limiter's answer for one request.
type Decision struct {
	Allowed    bool
	Remaining  int           // requests left right now
	RetryAfter time.Duration // until the next request is allowed
}

// Allow takes a token from key's bucket, if there's one left.
func (l *Limiter) Allow(ctx context.Context, key string) (Decision, error) {
	res, err := tokenBucket.Run(ctx, l.rdb, []string{"ratelimit:" + key},
		l.limit, l.period.Milliseconds()).Int64Slice()
	if err != nil {
		return Decision{}, fmt.Errorf("rate limit %s: %w", key, err)
	}
	return Decision{
		Allowed:    res[0] == 1,
		Remaining:  int(res[1]),
		RetryAfter: time.Duration(res[2]) * time.Millisecond,
	}, nil
}

// Middleware limits requests per client, as key names them.
func (l *Limiter) Middleware(key func(*http.Request) string) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d, err := l.Allow(r.Context(), key(r))
			if err != nil {
				// Fail open: Redis being down mustn't take the whole API down.
				slog.WarnContext(r.Context(), "rate limiter unavailable, request allowed", "err", err)
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(l.limit))
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(d.Remaining))
			if !d.Allowed {
				secs := math.Ceil(d.RetryAfter.Seconds()) // whole seconds, rounded up
				w.Header().Set("Retry-After", strconv.Itoa(int(secs)))
				httpx.Error(w, r, errRateLimited)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP keys public routes on the client's IP address.
func clientIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	return "ip:" + ip
}

// userID keys private routes on the user that authn.Require found.
func userID(r *http.Request) string {
	id, _ := authn.UserID(r.Context())
	return "user:" + id.String()
}
