package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/fiston-user/payments/db"
	walletv1 "github.com/fiston-user/payments/gen/wallet/v1"
	"github.com/fiston-user/payments/internal/fx"
	"github.com/fiston-user/payments/internal/platform/authn"
	"github.com/fiston-user/payments/internal/platform/config"
	"github.com/fiston-user/payments/internal/platform/grpcx"
	"github.com/fiston-user/payments/internal/platform/health"
	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/fiston-user/payments/internal/platform/idempotency"
	"github.com/fiston-user/payments/internal/platform/jobs"
	"github.com/fiston-user/payments/internal/platform/logging"
	"github.com/fiston-user/payments/internal/platform/natsx"
	"github.com/fiston-user/payments/internal/platform/outbox"
	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/fiston-user/payments/internal/platform/redis"
	"github.com/fiston-user/payments/internal/platform/telemetry"
	"github.com/fiston-user/payments/internal/wallet"
	"github.com/fiston-user/payments/internal/wallet/walletevents"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
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
		fmt.Fprintln(os.Stderr, "wallet:", err)
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
		Service: "wallet",
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
		version, err := postgres.Migrate(cfg.DatabaseURL, db.Migrations("wallet"))
		if err != nil {
			logger.Error("migrate", "err", err)
			os.Exit(1)
		}
		logger.Info("database migrated", "schema_version", version)
		return
	}

	// Traces go to the collector in OTEL_EXPORTER_OTLP_ENDPOINT, if there is one.
	stopTracing, err := telemetry.Setup(context.Background(),
		telemetry.Options{Service: "wallet", Version: version, Env: cfg.Env})
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
	wallet.RegisterMetrics(pool)

	// Redis is only a cache in front of Postgres. If it's down, we start
	// anyway, without the cache, and use it as soon as it's back.
	rdb, err := redis.NewClient(cfg.RedisURL)
	if err != nil {
		logger.Error("connect to redis", "err", err)
		os.Exit(1)
	}
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		logger.Warn("redis unreachable, idempotency keys use postgres only", "err", err)
	} else {
		logger.Info("connected to redis")
	}

	// NATS, for events. Like Postgres, and unlike Redis, it must be there when
	// we start: the stream has to exist.
	nc, err := natsx.Connect(cfg.NATSURL, "wallet", logger)
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
	if err := natsx.EnsureStream(context.Background(), js, walletevents.StreamConfig(cfg.NATSReplicas)); err != nil {
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

	// The wallet is ready when its database is. NATS isn't checked: while it's
	// down, events wait in the outbox, and payments carry on.
	probes := health.New(logger, map[string]health.Check{"postgres": pool.Ping})
	mux := http.NewServeMux()
	probes.Register(mux)
	// For Prometheus, which scrapes it every few seconds. The gateway never routes it.
	mux.Handle("GET /metrics", promhttp.Handler())

	// The wallet API, where every route needs an access token, and a JSON 404
	// for everything else.
	// Rates for currency conversion: built into the binary, for development.
	// In production, a Provider that asks a bank, or a market data feed.
	rates, err := fx.DevRates()
	if err != nil {
		logger.Error("fx rates", "err", err)
		os.Exit(1)
	}
	store := wallet.NewStore(pool)
	svc := wallet.NewService(store).WithFX(wallet.FX{
		Rates: rates, QuoteTTL: cfg.FXQuoteTTL, FeeBps: int64(cfg.FXFeeBps),
	})
	keys := idempotency.NewStore(pool, rdb)
	handler := wallet.NewHandler(svc, keys)
	api := http.NewServeMux()
	handler.Register(api)
	api.HandleFunc("/", httpx.NotFound)
	mux.Handle("/v1/", verifier.Require(httpx.Routes(api)))
	// Reports for finance: no user token, since users never reach them.
	// Episode 21 puts mTLS in front.
	handler.RegisterInternal(mux)
	mux.HandleFunc("/", httpx.NotFound)

	// The same service, over gRPC, for our other services. Every RPC needs an
	// access token, except health checks, and holds: sagas call those for
	// users who may be long gone. The gateway never routes to them.
	noToken := []string{
		healthpb.Health_ServiceDesc.ServiceName,
		walletv1.HoldService_ServiceDesc.ServiceName,
	}
	grpcSrv := grpcx.NewServer(logger, verifier.UnaryInterceptor(noToken...))
	walletv1.RegisterWalletServiceServer(grpcSrv, wallet.NewGRPCServer(svc, keys))
	walletv1.RegisterHoldServiceServer(grpcSrv, wallet.NewHoldServer(svc))
	healthSrv := grpchealth.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	// Reflection lets tools like grpcurl discover our API. Handy in
	// development; in production, the contract is the .proto file.
	if cfg.Env == "development" {
		reflection.Register(grpcSrv)
	}

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

	// Forget expired idempotency keys, once an hour.
	go keys.Cleanup(ctx, time.Hour)

	// Scheduled jobs: every replica campaigns, and only the leader runs them.
	// They stop the moment we're asked to, so another replica takes over while
	// we drain.
	runner := jobs.NewRunner(pool, logger, jobs.Job{
		Name:  "reconcile",
		Every: cfg.ReconcileEvery,
		At:    cfg.ReconcileAt,
		Run:   wallet.ReconcileJob(store, logger),
	})
	elector := jobs.NewElector(cfg.DatabaseURL, "wallet-jobs", logger)
	var leader sync.WaitGroup
	leader.Go(func() { elector.Run(ctx, runner.Run) })

	// The outbox relay publishes our events. It has a context of its own, so
	// it keeps publishing while the servers drain, and stops after them.
	relayCtx, stopRelay := context.WithCancel(context.Background())
	var relay sync.WaitGroup
	relay.Go(func() { outbox.NewRelay(pool, js, logger).Run(relayCtx) })

	go func() {
		logger.Info("wallet service listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		logger.Error("grpc listen", "err", err)
		os.Exit(1)
	}
	go func() {
		logger.Info("grpc server listening", "addr", cfg.GRPCAddr)
		if err := grpcSrv.Serve(lis); err != nil {
			logger.Error("grpc server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down gracefully")
	// Readiness fails first, over HTTP and gRPC, so load balancers stop sending
	// us new requests. Then both servers drain, within one deadline.
	healthSrv.Shutdown()
	probes.Drain(cfg.DrainDelay)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("http shutdown failed", "err", err)
		}
	})
	wg.Go(func() { grpcx.Shutdown(shutdownCtx, grpcSrv) })
	wg.Wait()
	logger.Info("servers stopped")
	// Then the relay. A batch it was in the middle of rolls back, and goes
	// out on the next start: at least once.
	stopRelay()
	relay.Wait()
	leader.Wait()
	logger.Info("outbox relay and jobs stopped")

	// Close the connections last: requests that were still finishing needed them.
	nc.Close()
	pool.Close()
	rdb.Close()
	logger.Info("nats, database pool and redis client closed")
	// Last, the spans still waiting in the batch, from everything that stopped.
	if err := stopTracing(shutdownCtx); err != nil {
		logger.Error("flush traces", "err", err)
	}
}
