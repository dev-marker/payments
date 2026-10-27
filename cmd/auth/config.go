package main

import (
	"log/slog"
	"time"

	"github.com/fiston-user/payments/internal/platform/config"
	"github.com/fiston-user/payments/internal/platform/logging"
)

// Config is everything the auth service reads from its environment.
type Config struct {
	Env             string // development or production
	HTTPAddr        string
	ShutdownTimeout time.Duration
	DrainDelay      time.Duration // how long readiness fails before we stop
	LogLevel        slog.Level
	LogFormat       string
	DatabaseURL     string
	DBMaxConns      int
	PrivateKeyFile  string
	AccessTTL       time.Duration
	RefreshTTL      time.Duration
	TLSDir          string // our certificate and key, and our CA's: for mTLS
	InternalAddr    string // the internal routes, over mTLS
}

// loadConfig reads the configuration. Everything has a sensible default except
// secrets, and every missing or invalid variable is reported at once.
func loadConfig(env *config.Env) (Config, error) {
	c := Config{
		Env:             env.OneOf("APP_ENV", "development", "development", "production"),
		HTTPAddr:        env.String("AUTH_HTTP_ADDR", ":8082"),
		ShutdownTimeout: env.Duration("AUTH_SHUTDOWN_TIMEOUT", 10*time.Second),
		DrainDelay:      env.Duration("AUTH_DRAIN_DELAY", 0),
		LogLevel:        config.Parse(env, "LOG_LEVEL", slog.LevelInfo, logging.ParseLevel),
		DatabaseURL:     env.Required("AUTH_DATABASE_URL"),
		DBMaxConns:      env.Int("AUTH_DB_MAX_CONNS", 10),
		PrivateKeyFile:  env.Required("AUTH_PRIVATE_KEY_FILE"),
		AccessTTL:       env.Duration("AUTH_ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTTL:      env.Duration("AUTH_REFRESH_TOKEN_TTL", 30*24*time.Hour),
		TLSDir:          env.String("AUTH_TLS_DIR", "secrets/tls/auth"),
		InternalAddr:    env.String("AUTH_INTERNAL_ADDR", ":9082"),
	}

	// Text logs for humans in development, JSON for log collectors in production.
	format := "text"
	if c.Env == "production" {
		format = "json"
	}
	c.LogFormat = env.OneOf("LOG_FORMAT", format, "text", "json")

	if c.DBMaxConns < 1 || c.DBMaxConns > 100 {
		env.Invalid("AUTH_DB_MAX_CONNS", "must be between 1 and 100")
	}
	// Nothing can take an access token back before it expires, so it must be short-lived.
	if c.AccessTTL < time.Minute || c.AccessTTL > time.Hour {
		env.Invalid("AUTH_ACCESS_TOKEN_TTL", "must be between 1m and 1h")
	}
	if c.RefreshTTL <= c.AccessTTL {
		env.Invalid("AUTH_REFRESH_TOKEN_TTL", "must be longer than AUTH_ACCESS_TOKEN_TTL")
	}
	return c, env.Err()
}
