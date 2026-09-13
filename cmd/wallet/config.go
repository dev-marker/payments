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
	LogLevel        slog.Level
	LogFormat       string
	DatabaseURL     string
	DBMaxConns      int
	RedisURL        string
	PublicKeyFile   string
}

// loadConfig reads the configuration. Everything has a sensible default except
// secrets, and every missing or invalid variable is reported at once.
func loadConfig(env *config.Env) (Config, error) {
	c := Config{
		Env:             env.OneOf("APP_ENV", "development", "development", "production"),
		HTTPAddr:        env.String("WALLET_HTTP_ADDR", ":8081"),
		GRPCAddr:        env.String("WALLET_GRPC_ADDR", ":9081"),
		ShutdownTimeout: env.Duration("WALLET_SHUTDOWN_TIMEOUT", 10*time.Second),
		LogLevel:        config.Parse(env, "LOG_LEVEL", slog.LevelInfo, logging.ParseLevel),
		DatabaseURL:     env.Required("WALLET_DATABASE_URL"),
		DBMaxConns:      env.Int("WALLET_DB_MAX_CONNS", 10),
		RedisURL:        env.String("WALLET_REDIS_URL", "redis://localhost:6379/0"),
		PublicKeyFile:   env.Required("WALLET_AUTH_PUBLIC_KEY_FILE"),
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
	return c, env.Err()
}
