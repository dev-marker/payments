package main

import (
	"log/slog"
	"time"

	"github.com/fiston-user/payments/internal/platform/config"
	"github.com/fiston-user/payments/internal/platform/logging"
	"github.com/fiston-user/payments/internal/transfer/risk"
)

// Config is everything the transfer service reads from its environment.
type Config struct {
	Env             string // development or production
	HTTPAddr        string
	ShutdownTimeout time.Duration
	DrainDelay      time.Duration // how long readiness fails before we stop
	LogLevel        slog.Level
	LogFormat       string
	DatabaseURL     string
	DBMaxConns      int
	WalletAddr      string // the wallet's gRPC API
	PublicKeyFile   string
	NATSURL         string
	NATSReplicas    int           // copies of the TRANSFER stream: 1 locally, 3 in production
	RedisURL        string        // the risk checks' velocity counters
	ReviewTimeout   time.Duration // then a transfer in review is rejected
	Risk            risk.Limits
}

// loadConfig reads the configuration. Everything has a sensible default except
// secrets, and every missing or invalid variable is reported at once.
func loadConfig(env *config.Env) (Config, error) {
	c := Config{
		Env:             env.OneOf("APP_ENV", "development", "development", "production"),
		HTTPAddr:        env.String("TRANSFER_HTTP_ADDR", ":8083"),
		ShutdownTimeout: env.Duration("TRANSFER_SHUTDOWN_TIMEOUT", 10*time.Second),
		DrainDelay:      env.Duration("TRANSFER_DRAIN_DELAY", 0),
		LogLevel:        config.Parse(env, "LOG_LEVEL", slog.LevelInfo, logging.ParseLevel),
		DatabaseURL:     env.Required("TRANSFER_DATABASE_URL"),
		DBMaxConns:      env.Int("TRANSFER_DB_MAX_CONNS", 10),
		WalletAddr:      env.String("TRANSFER_WALLET_ADDR", "dns:///localhost:9081"),
		PublicKeyFile:   env.Required("TRANSFER_AUTH_PUBLIC_KEY_FILE"),
		NATSURL:         env.String("TRANSFER_NATS_URL", "nats://localhost:4222"),
		NATSReplicas:    env.Int("TRANSFER_NATS_REPLICAS", 1),
		RedisURL:        env.String("TRANSFER_REDIS_URL", "redis://localhost:6379/0"),
		ReviewTimeout:   env.Duration("TRANSFER_REVIEW_TIMEOUT", 24*time.Hour),
		Risk: risk.Limits{
			MaxCount:       env.Int("TRANSFER_RISK_MAX_COUNT", 5),
			CountWindow:    env.Duration("TRANSFER_RISK_COUNT_WINDOW", 10*time.Minute),
			MaxAmount:      int64(env.Int("TRANSFER_RISK_MAX_AMOUNT", 200_000)),
			AmountWindow:   env.Duration("TRANSFER_RISK_AMOUNT_WINDOW", 24*time.Hour),
			ReviewAbove:    int64(env.Int("TRANSFER_RISK_REVIEW_ABOVE", 100_000)),
			FirstTimeAbove: int64(env.Int("TRANSFER_RISK_FIRST_TIME_ABOVE", 25_000)),
			NewWalletAge:   env.Duration("TRANSFER_RISK_NEW_WALLET_AGE", 24*time.Hour),
		},
	}

	// Text logs for humans in development, JSON for log collectors in production.
	format := "text"
	if c.Env == "production" {
		format = "json"
	}
	c.LogFormat = env.OneOf("LOG_FORMAT", format, "text", "json")

	if c.DBMaxConns < 1 || c.DBMaxConns > 100 {
		env.Invalid("TRANSFER_DB_MAX_CONNS", "must be between 1 and 100")
	}
	if c.NATSReplicas < 1 || c.NATSReplicas > 5 {
		env.Invalid("TRANSFER_NATS_REPLICAS", "must be between 1 and 5")
	}
	if c.Risk.CountWindow > c.Risk.AmountWindow {
		env.Invalid("TRANSFER_RISK_COUNT_WINDOW", "must not be longer than TRANSFER_RISK_AMOUNT_WINDOW")
	}
	return c, env.Err()
}
