package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/fiston-user/payments/internal/platform/authn"
	"github.com/fiston-user/payments/internal/platform/config"
	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/fiston-user/payments/internal/platform/logging"
	"github.com/fiston-user/payments/internal/platform/natsx"
	"github.com/fiston-user/payments/internal/platform/outbox"
	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/fiston-user/payments/internal/transfer"
	"github.com/fiston-user/payments/internal/transfer/transferevents"
	"github.com/fiston-user/payments/internal/wallet/walletclient"
	"github.com/nats-io/nats.go/jetstream"
)

// version is stamped at build time: go build -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	cfg, err := loadConfig(config.New(os.LookupEnv))
	if err != nil {
		// There's no logger yet: its settings are part of the configuration.
		fmt.Fprintln(os.Stderr, "transfer:", err)
		os.Exit(2)
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

	pool, err := postgres.NewPool(context.Background(), cfg.DatabaseURL, int32(cfg.DBMaxConns))
	if err != nil {
		logger.Error("connect to postgres", "err", err)
		os.Exit(1)
	}
	logger.Info("connected to postgres", "max_conns", pool.Config().MaxConns)

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

	// The wallet's gRPC API. Each call gets three seconds, retries included.
	wallet, err := walletclient.New(cfg.WalletAddr, 3*time.Second)
	if err != nil {
		logger.Error("wallet client", "err", err)
		os.Exit(1)
	}
	store := transfer.NewStore(pool)
	orchestrator := transfer.NewOrchestrator(store, transfer.NewWallet(wallet), logger)

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

	// The transfer API, where every route needs an access token. A new
	// transfer wakes the orchestrator up.
	api := http.NewServeMux()
	transfer.NewHandler(store, orchestrator.Wake).Register(api)
	api.HandleFunc("/", httpx.NotFound)
	mux.Handle("/v1/", verifier.Require(api))
	mux.HandleFunc("/", httpx.NotFound)

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

	<-ctx.Done()
	logger.Info("shutting down gracefully")

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
	nc.Close()
	pool.Close()
	logger.Info("wallet client, nats and database pool closed")
}
