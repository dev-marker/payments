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

	"github.com/fiston-user/payments/internal/gateway"
	"github.com/fiston-user/payments/internal/platform/authn"
	"github.com/fiston-user/payments/internal/platform/config"
	"github.com/fiston-user/payments/internal/platform/health"
	"github.com/fiston-user/payments/internal/platform/logging"
	"github.com/fiston-user/payments/internal/platform/redis"
	"github.com/fiston-user/payments/internal/platform/telemetry"
	"github.com/fiston-user/payments/internal/wallet/walletclient"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// version is stamped at build time: go build -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	healthcheck := flag.Bool("healthcheck", false, "exit 0 if the service is ready: for Docker")
	flag.Parse()
	cfg, err := loadConfig(config.New(os.LookupEnv))
	if err != nil {
		// There's no logger yet: its settings are part of the configuration.
		fmt.Fprintln(os.Stderr, "gateway:", err)
		os.Exit(2)
	}
	// Docker's health check: the binary checks itself, then exits.
	if *healthcheck {
		if err := health.Probe(cfg.MetricsAddr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	logger := logging.New(os.Stdout, logging.Options{
		Service: "gateway",
		Version: version,
		Level:   cfg.LogLevel,
		Format:  cfg.LogFormat,
	})
	slog.SetDefault(logger)
	logger.Info("starting", "env", cfg.Env, "log_level", cfg.LogLevel.String())

	// Traces go to the collector in OTEL_EXPORTER_OTLP_ENDPOINT, if there is one.
	stopTracing, err := telemetry.Setup(context.Background(),
		telemetry.Options{Service: "gateway", Version: version, Env: cfg.Env})
	if err != nil {
		logger.Error("telemetry", "err", err)
		os.Exit(1)
	}

	pub, err := authn.ReadPublicKey(cfg.PublicKeyFile)
	if err != nil {
		logger.Error("read auth public key", "err", err)
		os.Exit(1)
	}
	// Redis only holds rate limits. Down, it costs us the limits, not the API.
	rdb, err := redis.NewClient(cfg.RedisURL)
	if err != nil {
		logger.Error("connect to redis", "err", err)
		os.Exit(1)
	}
	// One client for the whole process: every request shares its connection.
	wallet, err := walletclient.New(cfg.WalletAddr, 5*time.Second)
	if err != nil {
		logger.Error("wallet client", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: gateway.New(gateway.Config{
			AuthURL:   cfg.AuthURL,
			Transfers: cfg.TransferURL,
			Wallet:    wallet,
			Verifier:  authn.NewVerifier(pub),
			Redis:     rdb,
			IPLimit:   cfg.IPLimit,
			UserLimit: cfg.UserLimit,
			Origins:   cfg.Origins,
			Logger:    logger,
		}),
		// We face the internet: every phase of a request has a limit.
		ReadHeaderTimeout: 5 * time.Second,  // slowloris: headers sent a byte at a time
		ReadTimeout:       10 * time.Second, // the whole request, body included
		WriteTimeout:      15 * time.Second, // longer than any upstream timeout
		IdleTimeout:       60 * time.Second, // keep-alive connections between requests
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Metrics and probes, on a port of their own: the gateway's port faces the
	// internet. It checks nothing else: if it checked the services behind it,
	// one of them down would take every gateway out with it.
	probes := health.New(logger, nil)
	ops := http.NewServeMux()
	ops.Handle("GET /metrics", promhttp.Handler())
	probes.Register(ops)
	metrics := &http.Server{Addr: cfg.MetricsAddr, Handler: ops, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := metrics.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics server failed", "err", err)
			os.Exit(1)
		}
	}()

	go func() {
		logger.Info("gateway listening", "addr", srv.Addr,
			"auth", cfg.AuthURL.String(), "wallet", cfg.WalletAddr, "transfer", cfg.TransferURL.String())
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
	metrics.Shutdown(shutdownCtx)
	logger.Info("server stopped")

	wallet.Close()
	rdb.Close()
	logger.Info("wallet and redis clients closed")
	// Last, the spans still waiting in the batch, from everything that stopped.
	if err := stopTracing(shutdownCtx); err != nil {
		logger.Error("flush traces", "err", err)
	}
}
