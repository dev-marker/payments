// Package redis connects services to Redis.
package redis

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// NewClient returns a client for the Redis server at url, like
// redis://localhost:6379/0. It connects lazily, and reconnects by itself, so a
// Redis that's down now is used again as soon as it's back.
func NewClient(url string) (*goredis.Client, error) {
	opts, err := goredis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("redis: parse url: %w", err)
	}
	// We only use Redis as a cache, so it must fail fast, without retries: a
	// slow cache that makes every request slow is worse than no cache at all.
	opts.DialTimeout = 500 * time.Millisecond
	opts.ReadTimeout = 200 * time.Millisecond
	opts.WriteTimeout = 200 * time.Millisecond
	opts.DialerRetries = 1
	opts.MaxRetries = -1
	goredis.SetLogger(logger{})
	return goredis.NewClient(opts), nil
}

// logger sends go-redis's own log lines to slog, at debug level: our code
// already logs every failure that matters, with the request's context.
type logger struct{}

func (logger) Printf(ctx context.Context, format string, v ...any) {
	slog.DebugContext(ctx, fmt.Sprintf(format, v...), "component", "go-redis")
}
