package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fiston-user/payments/db"
	"github.com/fiston-user/payments/internal/auth"
	"github.com/fiston-user/payments/internal/platform/authn"
	"github.com/fiston-user/payments/internal/platform/config"
	"github.com/fiston-user/payments/internal/platform/health"
	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/fiston-user/payments/internal/platform/logging"
	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/fiston-user/payments/internal/platform/telemetry"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// version is stamped at build time: go build -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	healthcheck := flag.Bool("healthcheck", false, "exit 0 if the service is ready: for Docker")
	migrate := flag.Bool("migrate", false, "apply the database migrations, and exit")
	flag.Parse()
	cfg, err := loadConfig(config.New(os.LookupEnv))
	if err != nil {
		// There's no logger yet: its settings are part of the configuration.
		fmt.Fprintln(os.Stderr, "auth:", err)
		os.Exit(2)
	}
	// Docker's health check: the binary checks itself, then exits.
	if *healthcheck {
		if err := health.Probe(cfg.HTTPAddr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	logger := logging.New(os.Stdout, logging.Options{
		Service: "auth",
		Version: version,
		Level:   cfg.LogLevel,
		Format:  cfg.LogFormat,
	})
	// Code that logs through the slog package functions gets our logger too.
	slog.SetDefault(logger)
	logger.Info("starting", "env", cfg.Env, "log_level", cfg.LogLevel.String())

	// Apply the migrations, then exit: the one-shot step before a new version
	// starts, in Compose, and in Kubernetes.
	if *migrate {
		version, err := postgres.Migrate(cfg.DatabaseURL, db.Migrations("auth"))
		if err != nil {
			logger.Error("migrate", "err", err)
			os.Exit(1)
		}
		logger.Info("database migrated", "schema_version", version)
		return
	}

	// Traces go to the collector in OTEL_EXPORTER_OTLP_ENDPOINT, if there is one.
	stopTracing, err := telemetry.Setup(context.Background(),
		telemetry.Options{Service: "auth", Version: version, Env: cfg.Env})
	if err != nil {
		logger.Error("telemetry", "err", err)
		os.Exit(1)
	}

	pool, err := postgres.NewPool(context.Background(), cfg.DatabaseURL, int32(cfg.DBMaxConns))
	if err != nil {
		logger.Error("connect to postgres", "err", err)
		os.Exit(1)
	}
	logger.Info("connected to postgres", "max_conns", pool.Config().MaxConns)

	// The private key signs access tokens. Without it, we can't do our job.
	key, err := auth.ReadPrivateKey(cfg.PrivateKeyFile)
	if err != nil {
		logger.Error("read signing key", "err", err)
		os.Exit(1)
	}
	signer := auth.NewSigner(key, cfg.AccessTTL)
	logger.Info("signing key loaded", "kid", authn.KeyID(signer.PublicKey()))

	probes := health.New(logger, map[string]health.Check{"postgres": pool.Ping})
	mux := http.NewServeMux()
	probes.Register(mux)
	// For Prometheus, which scrapes it every few seconds. The gateway never routes it.
	mux.Handle("GET /metrics", promhttp.Handler())

	// The auth API, and a JSON 404 for everything else.
	svc := auth.NewService(pool, signer, cfg.RefreshTTL)
	auth.NewHandler(svc, authn.NewVerifier(signer.PublicKey())).Register(mux)
	mux.HandleFunc("/", httpx.NotFound)

	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpx.Chain(httpx.Routes(mux),
			httpx.Observe(),
			httpx.RequestID,
			httpx.AccessLog(logger),
			httpx.Recover(logger),
		),
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("auth service listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down gracefully")
	// Readiness fails first, so load balancers stop sending us new requests.
	probes.Drain(cfg.DrainDelay)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown failed", "err", err)
	}
	logger.Info("server stopped")

	// Close the pool last: requests that were still finishing needed it.
	pool.Close()
	logger.Info("database pool closed")
	// Last, the spans still waiting in the batch, from everything that stopped.
	if err := stopTracing(shutdownCtx); err != nil {
		logger.Error("flush traces", "err", err)
	}
}
