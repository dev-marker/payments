package main

import (
	"context"
	"errors"
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
	"github.com/fiston-user/payments/internal/platform/logging"
	"github.com/fiston-user/payments/internal/platform/redis"
	"github.com/fiston-user/payments/internal/wallet/walletclient"
)

// version is stamped at build time: go build -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	cfg, err := loadConfig(config.New(os.LookupEnv))
	if err != nil {
		// There's no logger yet: its settings are part of the configuration.
		fmt.Fprintln(os.Stderr, "gateway:", err)
		os.Exit(2)
	}
	logger := logging.New(os.Stdout, logging.Options{
		Service: "gateway",
		Version: version,
		Level:   cfg.LogLevel,
		Format:  cfg.LogFormat,
	})
	slog.SetDefault(logger)
	logger.Info("starting", "env", cfg.Env, "log_level", cfg.LogLevel.String())

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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown failed", "err", err)
	}
	logger.Info("server stopped")

	wallet.Close()
	rdb.Close()
	logger.Info("wallet and redis clients closed")
}
