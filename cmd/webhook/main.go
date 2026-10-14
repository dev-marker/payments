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
	"sync"
	"syscall"
	"time"

	"github.com/fiston-user/payments/db"
	"github.com/fiston-user/payments/internal/platform/authn"
	"github.com/fiston-user/payments/internal/platform/config"
	"github.com/fiston-user/payments/internal/platform/health"
	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/fiston-user/payments/internal/platform/logging"
	"github.com/fiston-user/payments/internal/platform/natsx"
	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/fiston-user/payments/internal/platform/telemetry"
	"github.com/fiston-user/payments/internal/webhook"
	"github.com/fiston-user/payments/internal/webhook/webhookdb"
	"github.com/nats-io/nats.go/jetstream"
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
		fmt.Fprintln(os.Stderr, "webhook:", err)
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
		Service: "webhook",
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
		version, err := postgres.Migrate(cfg.DatabaseURL, db.Migrations("webhook"))
		if err != nil {
			logger.Error("migrate", "err", err)
			os.Exit(1)
		}
		logger.Info("database migrated", "schema_version", version)
		return
	}

	// Traces go to the collector in OTEL_EXPORTER_OTLP_ENDPOINT, if there is one.
	stopTracing, err := telemetry.Setup(context.Background(),
		telemetry.Options{Service: "webhook", Version: version, Env: cfg.Env})
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

	// The auth service's public key: every route checks the caller's token,
	// like the transfer service's.
	pub, err := authn.ReadPublicKey(cfg.PublicKeyFile)
	if err != nil {
		logger.Error("read auth public key", "err", err)
		os.Exit(1)
	}
	verifier := authn.NewVerifier(pub)

	// Deliveries go out through a client that, in production, refuses to
	// connect to our own network.
	production := cfg.Env == "production"
	q := webhookdb.New(pool)
	worker := webhook.NewWorker(q, webhook.NewClient(production), logger)
	worker.Schedule = cfg.RetrySchedule

	// NATS, for events. Like Postgres, it must be there when we start.
	nc, err := natsx.Connect(cfg.NATSURL, "webhook", logger)
	if err != nil {
		logger.Error("connect to nats", "err", err)
		os.Exit(1)
	}
	logger.Info("connected to nats", "url", nc.ConnectedUrl())
	js, err := jetstream.New(nc)
	if err != nil {
		logger.Error("jetstream", "err", err)
		os.Exit(1)
	}

	// Its work arrives through NATS, so here, NATS is part of being ready.
	probes := health.New(logger, map[string]health.Check{
		"postgres": pool.Ping,
		"nats":     natsx.Connected(nc),
	})
	mux := http.NewServeMux()
	probes.Register(mux)
	// For Prometheus, which scrapes it every few seconds. The gateway never routes it.
	mux.Handle("GET /metrics", promhttp.Handler())

	// The webhook API, where every route needs an access token.
	api := http.NewServeMux()
	webhook.NewAPI(q, production, worker.Wake).Register(api)
	api.HandleFunc("/", httpx.NotFound)
	mux.Handle("/v1/", verifier.Require(httpx.Routes(api)))

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

	// Transfer events become deliveries, and the worker sends them. It has a
	// context of its own: it keeps sending while the server drains.
	cc, err := webhook.NewFanOut(q, worker.Wake, logger).Consume(ctx, js)
	if err != nil {
		logger.Error("consume", "err", err)
		os.Exit(1)
	}
	workerCtx, stopWorker := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { worker.Run(workerCtx) })

	go func() {
		logger.Info("webhook service listening", "addr", srv.Addr)
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
		logger.Error("http shutdown failed", "err", err)
	}
	logger.Info("server stopped")
	// Stop pulling events, and finish the ones we have. Then the worker
	// finishes its batch, and stops.
	cc.Drain()
	<-cc.Closed()
	stopWorker()
	wg.Wait()
	logger.Info("consumer and worker stopped")

	nc.Close()
	pool.Close()
	logger.Info("nats and database pool closed")
	// Last, the spans still waiting in the batch, from everything that stopped.
	if err := stopTracing(shutdownCtx); err != nil {
		logger.Error("flush traces", "err", err)
	}
}
