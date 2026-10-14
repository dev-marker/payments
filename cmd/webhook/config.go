package main

import (
	"log/slog"
	"strings"
	"time"

	"github.com/fiston-user/payments/internal/platform/config"
	"github.com/fiston-user/payments/internal/platform/logging"
	"github.com/fiston-user/payments/internal/webhook"
)

// Config is everything the webhook service reads from its environment.
type Config struct {
	Env             string // development or production
	HTTPAddr        string
	ShutdownTimeout time.Duration
	DrainDelay      time.Duration // how long readiness fails before we stop
	LogLevel        slog.Level
	LogFormat       string
	DatabaseURL     string
	DBMaxConns      int
	NATSURL         string
	PublicKeyFile   string
	RetrySchedule   []time.Duration // the wait before each retry
}

// loadConfig reads the configuration. Everything has a sensible default except
// secrets, and every missing or invalid variable is reported at once.
func loadConfig(env *config.Env) (Config, error) {
	c := Config{
		Env:             env.OneOf("APP_ENV", "development", "development", "production"),
		HTTPAddr:        env.String("WEBHOOK_HTTP_ADDR", ":8085"),
		ShutdownTimeout: env.Duration("WEBHOOK_SHUTDOWN_TIMEOUT", 10*time.Second),
		DrainDelay:      env.Duration("WEBHOOK_DRAIN_DELAY", 0),
		LogLevel:        config.Parse(env, "LOG_LEVEL", slog.LevelInfo, logging.ParseLevel),
		DatabaseURL:     env.Required("WEBHOOK_DATABASE_URL"),
		DBMaxConns:      env.Int("WEBHOOK_DB_MAX_CONNS", 10),
		NATSURL:         env.String("WEBHOOK_NATS_URL", "nats://localhost:4222"),
		PublicKeyFile:   env.Required("WEBHOOK_AUTH_PUBLIC_KEY_FILE"),
		RetrySchedule:   config.Parse(env, "WEBHOOK_RETRY_SCHEDULE", webhook.Schedule, parseDurations),
	}

	// Text logs for humans in development, JSON for log collectors in production.
	format := "text"
	if c.Env == "production" {
		format = "json"
	}
	c.LogFormat = env.OneOf("LOG_FORMAT", format, "text", "json")

	if c.DBMaxConns < 1 || c.DBMaxConns > 100 {
		env.Invalid("WEBHOOK_DB_MAX_CONNS", "must be between 1 and 100")
	}
	return c, env.Err()
}

// parseDurations reads a list of durations, like 5s,1m,1h.
func parseDurations(s string) ([]time.Duration, error) {
	var ds []time.Duration
	for f := range strings.SplitSeq(s, ",") {
		d, err := time.ParseDuration(strings.TrimSpace(f))
		if err != nil {
			return nil, err
		}
		ds = append(ds, d)
	}
	return ds, nil
}
