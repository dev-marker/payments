// Package gateway is the payments API's front door. It routes requests to
// our services, and holds no business rules: the services decide everything.
package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/fiston-user/payments/internal/platform/authn"
	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/fiston-user/payments/internal/wallet/walletclient"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Config is what the gateway needs: services, token checks and limits.
type Config struct {
	AuthURL   *url.URL             // the auth service, over HTTP
	Transfers *url.URL             // the transfer service, over HTTP
	Webhooks  *url.URL             // the webhook service, over HTTP
	WalletAPI *url.URL             // the wallet's REST API, for conversions
	Wallet    *walletclient.Client // the wallet service, over gRPC
	Verifier  *authn.Verifier
	Redis     *redis.Client // the rate limiters' buckets
	IPLimit   int           // requests per minute from one IP address, to public routes
	UserLimit int           // requests per minute from one user
	Origins   []string      // browser apps that may call us
	Logger    *slog.Logger
}

// How long each service gets to answer. Auth hashes passwords, slowly.
const (
	authTimeout     = 5 * time.Second
	walletTimeout   = 3 * time.Second
	transferTimeout = 3 * time.Second
	webhookTimeout  = 3 * time.Second
	reportTimeout   = time.Minute // a year of entries
)

// New returns the gateway's handler.
func New(cfg Config) http.Handler {
	byIP := NewLimiter(cfg.Redis, cfg.IPLimit, time.Minute).Middleware(clientIP)
	byUser := NewLimiter(cfg.Redis, cfg.UserLimit, time.Minute).Middleware(userID)
	// Public routes are limited per IP. Private ones need a token first.
	public := func(d time.Duration, h http.Handler) http.Handler {
		return httpx.Chain(h, byIP, timeout(d))
	}
	private := func(d time.Duration, h http.Handler) http.Handler {
		return httpx.Chain(h, cfg.Verifier.Require, byUser, timeout(d))
	}

	mux := http.NewServeMux()

	auth := newProxy(cfg.AuthURL)
	mux.Handle("POST /v1/auth/register", public(authTimeout, auth))
	mux.Handle("POST /v1/auth/login", public(authTimeout, auth))
	mux.Handle("POST /v1/auth/refresh", public(authTimeout, auth))
	mux.Handle("/v1/auth/", private(authTimeout, auth))

	wallet := &walletAPI{client: cfg.Wallet}
	mux.Handle("POST /v1/wallets", private(walletTimeout, rpc(wallet.create)))
	mux.Handle("GET /v1/wallets/{id}", private(walletTimeout, rpc(wallet.get)))
	mux.Handle("GET /v1/wallets/{id}/balance", private(walletTimeout, rpc(wallet.balance)))
	mux.Handle("POST /v1/wallets/{id}/deposits", private(walletTimeout, rpc(wallet.deposit)))
	// Statements are files, not messages: the wallet's REST API streams them.
	mux.Handle("GET /v1/wallets/{id}/statement.csv", private(reportTimeout, newProxy(cfg.WalletAPI)))
	// Quotes and conversions: only our users call them, so they stay REST.
	mux.Handle("/v1/fx/", private(walletTimeout, newProxy(cfg.WalletAPI)))

	// Transfers are sagas now: the transfer service runs them.
	transfers := newProxy(cfg.Transfers)
	mux.Handle("POST /v1/transfers", private(transferTimeout, transfers))
	mux.Handle("GET /v1/transfers/{id}", private(transferTimeout, transfers))

	// Webhook endpoints and their deliveries: the webhook service checks the rest.
	mux.Handle("/v1/webhooks/", private(webhookTimeout, newProxy(cfg.Webhooks)))
	mux.HandleFunc("/", httpx.NotFound)

	// Observe goes first. We face the internet, so a client's trace context
	// is only linked, never continued: nobody outside decides what we sample.
	edge := otelhttp.WithPublicEndpointFn(func(*http.Request) bool { return true })
	return httpx.Chain(httpx.Routes(mux),
		httpx.Observe(edge),
		httpx.RequestID,
		httpx.AccessLog(cfg.Logger),
		httpx.Recover(cfg.Logger),
		cors(cfg.Origins),
		limitBody(64<<10),
	)
}

// timeout gives each request a deadline, which its context carries upstream.
func timeout(d time.Duration) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// limitBody caps request bodies at n bytes, the proxied ones too.
func limitBody(n int64) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}
