package main

import (
	"log/slog"
	"time"

	"github.com/fiston-user/payments/internal/platform/config"
	"github.com/fiston-user/payments/internal/platform/logging"
)

// Config is everything the notification service reads from its environment.
type Config struct {
	Env             string // development or production
	HTTPAddr        string
	ShutdownTimeout time.Duration
	LogLevel        slog.Level
	LogFormat       string
	DatabaseURL     string
	DBMaxConns      int
	NATSURL         string
	AuthURL         string // the auth service, which knows email addresses
	SMTPAddr        string // where email goes: Mailpit, in development
}

// loadConfig reads the configuration. Everything has a sensible default except
// secrets, and every missing or invalid variable is reported at once.
func loadConfig(env *config.Env) (Config, error) {
	c := Config{
		Env:             env.OneOf("APP_ENV", "development", "development", "production"),
		HTTPAddr:        env.String("NOTIFICATION_HTTP_ADDR", ":8084"),
		ShutdownTimeout: env.Duration("NOTIFICATION_SHUTDOWN_TIMEOUT", 10*time.Second),
		LogLevel:        config.Parse(env, "LOG_LEVEL", slog.LevelInfo, logging.ParseLevel),
		DatabaseURL:     env.Required("NOTIFICATION_DATABASE_URL"),
		DBMaxConns:      env.Int("NOTIFICATION_DB_MAX_CONNS", 10),
		NATSURL:         env.String("NOTIFICATION_NATS_URL", "nats://localhost:4222"),
		AuthURL:         env.String("NOTIFICATION_AUTH_URL", "http://localhost:8082"),
		SMTPAddr:        env.String("NOTIFICATION_SMTP_ADDR", "localhost:1025"),
	}

	// Text logs for humans in development, JSON for log collectors in production.
	format := "text"
	if c.Env == "production" {
		format = "json"
	}
	c.LogFormat = env.OneOf("LOG_FORMAT", format, "text", "json")

	if c.DBMaxConns < 1 || c.DBMaxConns > 100 {
		env.Invalid("NOTIFICATION_DB_MAX_CONNS", "must be between 1 and 100")
	}
	return c, env.Err()
}
