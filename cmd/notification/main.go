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
	"github.com/fiston-user/payments/internal/notification"
	"github.com/fiston-user/payments/internal/platform/config"
	"github.com/fiston-user/payments/internal/platform/health"
	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/fiston-user/payments/internal/platform/logging"
	"github.com/fiston-user/payments/internal/platform/mtls"
	"github.com/fiston-user/payments/internal/platform/natsx"
	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/fiston-user/payments/internal/platform/telemetry"
	"github.com/fiston-user/payments/internal/transfer/transferevents"
	"github.com/fiston-user/payments/internal/wallet/walletevents"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// version is stamped at build time: go build -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	replay := flag.Bool("replay-dead-letters", false, "send the dead letters again, and exit")
	healthcheck := flag.Bool("healthcheck", false, "exit 0 if the service is ready: for Docker")
	migrate := flag.Bool("migrate", false, "apply the database migrations, and exit")
	flag.Parse()
	cfg, err := loadConfig(config.New(os.LookupEnv))
	if err != nil {
		// There's no logger yet: its settings are part of the configuration.
		fmt.Fprintln(os.Stderr, "notification:", err)
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
		Service: "notification",
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
		version, err := postgres.Migrate(cfg.DatabaseURL, db.Migrations("notification"))
		if err != nil {
			logger.Error("migrate", "err", err)
			os.Exit(1)
		}
		logger.Info("database migrated", "schema_version", version)
		return
	}

	// Traces go to the collector in OTEL_EXPORTER_OTLP_ENDPOINT, if there is one.
	stopTracing, err := telemetry.Setup(context.Background(),
		telemetry.Options{Service: "notification", Version: version, Env: cfg.Env})
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

	// Email goes out through SMTP, to the addresses the auth service knows,
	// cached for a minute. We ask over mTLS: auth knows us by our certificate.
	certs, err := mtls.Load(cfg.TLSDir)
	if err != nil {
		logger.Error("load certificates", "err", err)
		os.Exit(1)
	}
	toAuth := &http.Transport{TLSClientConfig: certs.Client("auth")}
	notifier := notification.NewNotifier(
		notification.NewStore(pool),
		notification.NewCachedUsers(notification.AuthUsers{BaseURL: cfg.AuthURL, Client: &http.Client{
			Timeout:   5 * time.Second,
			Transport: otelhttp.NewTransport(toAuth), // a span, and the context
		}}, time.Minute),
		notification.SMTP{Addr: cfg.SMTPAddr, From: "no-reply@payments.example"},
		logger,
	)
	if *replay {
		n, err := notifier.Replay(context.Background())
		pool.Close()
		if err != nil {
			logger.Error("replay dead letters", "err", err)
			os.Exit(1)
		}
		logger.Info("dead letters replayed", "sent", n)
		return
	}

	// NATS, for events. Like Postgres, it must be there when we start.
	nc, err := natsx.Connect(cfg.NATSURL, "notification", logger)
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

	// One durable consumer on each stream we listen to. We don't create the
	// streams: the services that publish to them do.
	var consumers []jetstream.ConsumeContext
	for stream, subject := range map[string]string{
		transferevents.Stream: "transfer.events.>",
		walletevents.Stream:   walletevents.Subject(walletevents.TransferCompleted),
	} {
		cc, err := notifier.Consume(ctx, js, stream, subject)
		if err != nil {
			logger.Error("consume", "err", err)
			os.Exit(1)
		}
		consumers = append(consumers, cc)
	}

	go func() {
		logger.Info("notification service listening", "addr", srv.Addr)
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
	// Stop pulling, and finish the messages we already have. One that's cut
	// short is delivered again, later: at least once.
	for _, cc := range consumers {
		cc.Drain()
		<-cc.Closed()
	}
	logger.Info("consumers stopped")

	nc.Close()
	pool.Close()
	logger.Info("nats and database pool closed")
	// Last, the spans still waiting in the batch, from everything that stopped.
	if err := stopTracing(shutdownCtx); err != nil {
		logger.Error("flush traces", "err", err)
	}
}
