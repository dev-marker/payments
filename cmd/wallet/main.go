package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	walletv1 "github.com/fiston-user/payments/gen/wallet/v1"
	"github.com/fiston-user/payments/internal/platform/authn"
	"github.com/fiston-user/payments/internal/platform/config"
	"github.com/fiston-user/payments/internal/platform/grpcx"
	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/fiston-user/payments/internal/platform/idempotency"
	"github.com/fiston-user/payments/internal/platform/logging"
	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/fiston-user/payments/internal/platform/redis"
	"github.com/fiston-user/payments/internal/wallet"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

// version is stamped at build time: go build -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	cfg, err := loadConfig(config.New(os.LookupEnv))
	if err != nil {
		// There's no logger yet: its settings are part of the configuration.
		fmt.Fprintln(os.Stderr, "wallet:", err)
		os.Exit(2)
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

	pool, err := postgres.NewPool(context.Background(), cfg.DatabaseURL, int32(cfg.DBMaxConns))
	if err != nil {
		logger.Error("connect to postgres", "err", err)
		os.Exit(1)
	}
	logger.Info("connected to postgres", "max_conns", pool.Config().MaxConns)

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

	// The auth service's public key: enough to check access tokens, and
	// useless for making them.
	pub, err := authn.ReadPublicKey(cfg.PublicKeyFile)
	if err != nil {
		logger.Error("read auth public key", "err", err)
		os.Exit(1)
	}
	verifier := authn.NewVerifier(pub)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			logger.WarnContext(ctx, "not ready: database unreachable", "err", err)
			httpx.WriteJSON(w, r, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		httpx.WriteJSON(w, r, http.StatusOK, map[string]string{"status": "ready"})
	})

	// The wallet API, and a JSON 404 for everything else.
	svc := wallet.NewService(wallet.NewStore(pool))
	keys := idempotency.NewStore(pool, rdb)
	wallet.NewHandler(svc, keys).Register(mux)
	mux.HandleFunc("/", httpx.NotFound)

	// The same service, over gRPC, for our other services. Every RPC needs an
	// access token, except health checks.
	public := healthpb.Health_ServiceDesc.ServiceName
	grpcSrv := grpcx.NewServer(logger, verifier.UnaryInterceptor(public))
	walletv1.RegisterWalletServiceServer(grpcSrv, wallet.NewGRPCServer(svc, keys))
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	// Reflection lets tools like grpcurl discover our API. Handy in
	// development; in production, the contract is the .proto file.
	if cfg.Env == "development" {
		reflection.Register(grpcSrv)
	}

	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpx.Chain(mux,
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	// Health checks fail first, so load balancers stop sending us traffic.
	// Then both servers drain at the same time, within one deadline.
	healthSrv.Shutdown()
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("http shutdown failed", "err", err)
		}
	})
	wg.Go(func() { grpcx.Shutdown(shutdownCtx, grpcSrv) })
	wg.Wait()
	logger.Info("servers stopped")

	// Close the pool last: requests that were still finishing needed it.
	pool.Close()
	rdb.Close()
	logger.Info("database pool and redis client closed")
}
