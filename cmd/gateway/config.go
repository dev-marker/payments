package main

import (
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/fiston-user/payments/internal/platform/config"
	"github.com/fiston-user/payments/internal/platform/logging"
)

// Config is everything the gateway reads from its environment.
type Config struct {
	Env             string // development or production
	HTTPAddr        string
	ShutdownTimeout time.Duration
	LogLevel        slog.Level
	LogFormat       string
	WalletAddr      string
	RedisURL        string
	PublicKeyFile   string
	IPLimit         int
	UserLimit       int
	Origins         []string
	AuthURL         *url.URL
	TransferURL     *url.URL
}

// loadConfig reads the configuration. Everything has a sensible default except
// secrets, and every missing or invalid variable is reported at once.
func loadConfig(env *config.Env) (Config, error) {
	c := Config{
		Env:             env.OneOf("APP_ENV", "development", "development", "production"),
		HTTPAddr:        env.String("GATEWAY_HTTP_ADDR", ":8080"),
		ShutdownTimeout: env.Duration("GATEWAY_SHUTDOWN_TIMEOUT", 10*time.Second),
		LogLevel:        config.Parse(env, "LOG_LEVEL", slog.LevelInfo, logging.ParseLevel),
		WalletAddr:      env.String("GATEWAY_WALLET_ADDR", "dns:///localhost:9081"),
		RedisURL:        env.String("GATEWAY_REDIS_URL", "redis://localhost:6379/0"),
		PublicKeyFile:   env.Required("GATEWAY_AUTH_PUBLIC_KEY_FILE"),
		IPLimit:         env.Int("GATEWAY_IP_LIMIT", 20),
		UserLimit:       env.Int("GATEWAY_USER_LIMIT", 120),
		Origins:         strings.Split(env.String("GATEWAY_CORS_ORIGINS", "http://localhost:5173"), ","),
		AuthURL: config.Parse(env, "GATEWAY_AUTH_URL",
			&url.URL{Scheme: "http", Host: "localhost:8082"}, url.Parse),
		TransferURL: config.Parse(env, "GATEWAY_TRANSFER_URL",
			&url.URL{Scheme: "http", Host: "localhost:8083"}, url.Parse),
	}

	// Text logs for humans in development, JSON for log collectors in production.
	format := "text"
	if c.Env == "production" {
		format = "json"
	}
	c.LogFormat = env.OneOf("LOG_FORMAT", format, "text", "json")

	if c.IPLimit < 1 || c.UserLimit < 1 {
		env.Invalid("GATEWAY_IP_LIMIT, GATEWAY_USER_LIMIT", "must be at least 1 a minute")
	}
	return c, env.Err()
}
