package main

import (
	"log/slog"
	"time"

	"github.com/fiston-user/payments/internal/platform/config"
	"github.com/fiston-user/payments/internal/platform/logging"
)

// Config is everything the wallet service reads from its environment.
type Config struct {
	Env             string // development or production
	HTTPAddr        string
	GRPCAddr        string
	ShutdownTimeout time.Duration
	DrainDelay      time.Duration // how long readiness fails before we stop
	LogLevel        slog.Level
	LogFormat       string
	DatabaseURL     string
	DBMaxConns      int
	DBReportConns   int // a pool of their own for reports: statements, trial balances
	RedisURL        string
	PublicKeyFile   string
	NATSURL         string
	NATSReplicas    int           // copies of the WALLET stream: 1 locally, 3 in production
	FXQuoteTTL      time.Duration // how long a quote's price holds
	FXFeeBps        int           // our fee on conversions, in basis points
	ReconcileEvery  time.Duration // how often the ledger is reconciled
	ReconcileAt     time.Duration // how far into each period: 2h, with 24h, is 02:00 UTC
	TLSDir          string        // our certificate and key, and our CA's: for mTLS
	InternalAddr    string        // the internal routes, over mTLS
}

// loadConfig reads the configuration. Everything has a sensible default except
// secrets, and every missing or invalid variable is reported at once.
func loadConfig(env *config.Env) (Config, error) {
	c := Config{
		Env:             env.OneOf("APP_ENV", "development", "development", "production"),
		HTTPAddr:        env.String("WALLET_HTTP_ADDR", ":8081"),
		GRPCAddr:        env.String("WALLET_GRPC_ADDR", ":9081"),
		ShutdownTimeout: env.Duration("WALLET_SHUTDOWN_TIMEOUT", 10*time.Second),
		DrainDelay:      env.Duration("WALLET_DRAIN_DELAY", 0),
		LogLevel:        config.Parse(env, "LOG_LEVEL", slog.LevelInfo, logging.ParseLevel),
		DatabaseURL:     env.Required("WALLET_DATABASE_URL"),
		DBMaxConns:      env.Int("WALLET_DB_MAX_CONNS", 10),
		DBReportConns:   env.Int("WALLET_DB_REPORT_CONNS", 2),
		RedisURL:        env.String("WALLET_REDIS_URL", "redis://localhost:6379/0"),
		PublicKeyFile:   env.Required("WALLET_AUTH_PUBLIC_KEY_FILE"),
		NATSURL:         env.String("WALLET_NATS_URL", "nats://localhost:4222"),
		NATSReplicas:    env.Int("WALLET_NATS_REPLICAS", 1),
		FXQuoteTTL:      env.Duration("WALLET_FX_QUOTE_TTL", 30*time.Second),
		FXFeeBps:        env.Int("WALLET_FX_FEE_BPS", 50),
		ReconcileEvery:  env.Duration("WALLET_RECONCILE_EVERY", 24*time.Hour),
		ReconcileAt:     env.Duration("WALLET_RECONCILE_AT", 2*time.Hour),
		TLSDir:          env.String("WALLET_TLS_DIR", "secrets/tls/wallet"),
		InternalAddr:    env.String("WALLET_INTERNAL_ADDR", ":9091"),
	}

	// Text logs for humans in development, JSON for log collectors in production.
	format := "text"
	if c.Env == "production" {
		format = "json"
	}
	c.LogFormat = env.OneOf("LOG_FORMAT", format, "text", "json")

	if c.DBMaxConns < 1 || c.DBMaxConns > 100 {
		env.Invalid("WALLET_DB_MAX_CONNS", "must be between 1 and 100")
	}
	if c.DBReportConns < 1 || c.DBReportConns > c.DBMaxConns {
		env.Invalid("WALLET_DB_REPORT_CONNS", "must be between 1 and WALLET_DB_MAX_CONNS")
	}
	if c.NATSReplicas < 1 || c.NATSReplicas > 5 {
		env.Invalid("WALLET_NATS_REPLICAS", "must be between 1 and 5")
	}
	if c.FXFeeBps < 0 || c.FXFeeBps > 1000 {
		env.Invalid("WALLET_FX_FEE_BPS", "must be between 0 and 1000: ten percent")
	}
	if c.ReconcileEvery < time.Second {
		env.Invalid("WALLET_RECONCILE_EVERY", "must be at least 1s")
	}
	return c, env.Err()
}
