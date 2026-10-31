package main

import (
	"context"
	"crypto/tls"
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
	"github.com/fiston-user/payments/internal/fx"
	"github.com/fiston-user/payments/internal/platform/authn"
	"github.com/fiston-user/payments/internal/platform/config"
	"github.com/fiston-user/payments/internal/platform/debug"
	"github.com/fiston-user/payments/internal/platform/health"
	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/fiston-user/payments/internal/platform/logging"
	"github.com/fiston-user/payments/internal/platform/mtls"
	"github.com/fiston-user/payments/internal/platform/natsx"
	"github.com/fiston-user/payments/internal/platform/outbox"
	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/fiston-user/payments/internal/platform/redis"
	"github.com/fiston-user/payments/internal/platform/telemetry"
	"github.com/fiston-user/payments/internal/transfer"
	"github.com/fiston-user/payments/internal/transfer/risk"
	"github.com/fiston-user/payments/internal/transfer/transferevents"
	"github.com/fiston-user/payments/internal/wallet/walletclient"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc/credentials"
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
		fmt.Fprintln(os.Stderr, "transfer:", err)
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
		Service: "transfer",
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
		version, err := postgres.Migrate(cfg.DatabaseURL, db.Migrations("transfer"))
		if err != nil {
			logger.Error("migrate", "err", err)
			os.Exit(1)
		}
		logger.Info("database migrated", "schema_version", version)
		return
	}

	// Traces go to the collector in OTEL_EXPORTER_OTLP_ENDPOINT, if there is one.
	stopTracing, err := telemetry.Setup(context.Background(),
		telemetry.Options{Service: "transfer", Version: version, Env: cfg.Env})
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
	outbox.RegisterMetrics(pool)
	postgres.RegisterMetrics("main", pool)

	// NATS, for events. Like Postgres, it must be there when we start.
	nc, err := natsx.Connect(cfg.NATSURL, "transfer", logger)
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
	stream := transferevents.StreamConfig(cfg.NATSReplicas)
	if err := natsx.EnsureStream(context.Background(), js, stream); err != nil {
		logger.Error("create stream", "err", err)
		os.Exit(1)
	}

	// The auth service's public key: enough to check access tokens, and
	// useless for making them.
	pub, err := authn.ReadPublicKey(cfg.PublicKeyFile)
	if err != nil {
		logger.Error("read auth public key", "err", err)
		os.Exit(1)
	}
	verifier := authn.NewVerifier(pub)
	// Our certificate, and our CA's: for mTLS with our other services.
	certs, err := mtls.Load(cfg.TLSDir)
	if err != nil {
		logger.Error("load certificates", "err", err)
		os.Exit(1)
	}

	// The wallet's gRPC API, over mTLS. Each call gets three seconds, retries included.
	creds := credentials.NewTLS(certs.Client("wallet"))
	wallet, err := walletclient.New(cfg.WalletAddr, creds, 3*time.Second)
	if err != nil {
		logger.Error("wallet client", "err", err)
		os.Exit(1)
	}
	// Redis keeps the velocity counters. Like the gateway's limiter, it's never
	// a reason to be unready: without it, transfers go to review.
	rdb, err := redis.NewClient(cfg.RedisURL)
	if err != nil {
		logger.Error("redis", "err", err)
		os.Exit(1)
	}
	rates, err := fx.DevRates(cfg.Env)
	if err != nil {
		logger.Error("fx rates", "err", err)
		os.Exit(1)
	}
	store := transfer.NewStore(pool)
	store.ReviewTimeout = cfg.ReviewTimeout
	engine := risk.NewEngine(risk.Rules(cfg.Risk)...)
	velocity := risk.NewVelocity(rdb, cfg.Risk.CountWindow, cfg.Risk.AmountWindow)
	screener := transfer.NewRiskScreener(engine, velocity, wallet, store, rates, logger)
	orchestrator := transfer.NewOrchestrator(store, transfer.NewWallet(wallet), screener, logger)

	// Like the wallet: ready with its database. Events wait in the outbox.
	probes := health.New(logger, map[string]health.Check{"postgres": pool.Ping})
	mux := http.NewServeMux()
	probes.Register(mux)
	// For Prometheus, which scrapes it every few seconds. The gateway never routes it.
	mux.Handle("GET /metrics", promhttp.Handler())

	// The transfer API, where every route needs an access token. A new
	// transfer wakes the orchestrator up.
	api := http.NewServeMux()
	handler := transfer.NewHandler(store, orchestrator.Wake)
	handler.Register(api)
	api.HandleFunc("/", httpx.NotFound)
	mux.Handle("/v1/", verifier.Require(httpx.Routes(api)))
	// The review API, over mTLS: only our back office's tools, ops, may call
	// it, for a reviewer who signed in, with the reviewer role.
	internal := http.NewServeMux()
	handler.RegisterInternal(internal)
	internal.HandleFunc("/", httpx.NotFound)
	mux.Handle("/internal/", httpx.Chain(httpx.Routes(internal),
		mtls.Allow("ops"), verifier.Require, authn.RequireRole("reviewer")))
	// Go's profiler, for our ops tools: their certificate, and no reviewer.
	mux.Handle("/internal/debug/", mtls.Allow("ops")(debug.Handler()))
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

	// The orchestrator and the relay have contexts of their own: they keep
	// working while the server drains, and stop after it, in that order.
	orchCtx, stopOrch := context.WithCancel(context.Background())
	relayCtx, stopRelay := context.WithCancel(context.Background())
	var orch, relay sync.WaitGroup
	orch.Go(func() { orchestrator.Run(orchCtx) })
	relay.Go(func() { outbox.NewRelay(pool, js, logger).Run(relayCtx) })

	go func() {
		logger.Info("transfer service listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	// The internal port: the same routes, over mTLS. Only what comes in
	// through it gets past mtls.Allow.
	go func() {
		lis, err := tls.Listen("tcp", cfg.InternalAddr, certs.Server())
		if err == nil {
			err = srv.Serve(lis)
		}
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("internal server failed", "err", err)
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
	// Steps in flight finish, and are written down. Then the relay publishes
	// the events they wrote. Anything left over goes out on the next start.
	stopOrch()
	orch.Wait()
	stopRelay()
	relay.Wait()
	logger.Info("orchestrator and outbox relay stopped")

	wallet.Close()
	rdb.Close()
	nc.Close()
	pool.Close()
	logger.Info("wallet client, nats and database pool closed")
	// Last, the spans still waiting in the batch, from everything that stopped.
	if err := stopTracing(shutdownCtx); err != nil {
		logger.Error("flush traces", "err", err)
	}
}
