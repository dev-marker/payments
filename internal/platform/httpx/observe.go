package httpx

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// requestDuration holds all three RED metrics: its count is the rate, its
// code label the errors, and its buckets the duration.
var requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name: "http_request_duration_seconds",
	Help: "How long HTTP requests took, by method, route and status code.",
	// A millisecond to ten seconds: every timeout we have is shorter.
	Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
}, []string{"method", "route", "code"})

type routeKey struct{}

// Observe traces each request, and measures it. It must be the outermost
// middleware, so everything inside, like the access log, sees the span.
// A public server passes otelhttp.WithPublicEndpointFn: it starts new
// traces, whatever the client sends.
func Observe(opts ...otelhttp.Option) Middleware {
	opts = append(opts, otelhttp.WithFilter(func(r *http.Request) bool { return !quiet(r) }))
	traced := otelhttp.NewMiddleware("http", opts...)
	return func(next http.Handler) http.Handler {
		return traced(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if quiet(r) {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			route := new(string)
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), routeKey{}, route)))

			// Label by route, never by path: /v1/wallets/{id} is one series,
			// while every wallet ID would be a series of its own.
			if *route == "" {
				*route = "unmatched"
			}
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			span := trace.SpanFromContext(r.Context())
			span.SetName(r.Method + " " + *route)
			span.SetAttributes(attribute.String("http.route", *route))
			requestDuration.WithLabelValues(r.Method, *route, strconv.Itoa(rec.status)).
				Observe(time.Since(start).Seconds())
		}))
	}
}

// Routes wraps a ServeMux, and tells Observe which pattern matched, like
// /v1/wallets/{id}. With a mux inside a mux, the innermost one wins.
func Routes(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r) // which sets r.Pattern
		route, ok := r.Context().Value(routeKey{}).(*string)
		if i := strings.Index(r.Pattern, "/"); ok && *route == "" && i >= 0 {
			*route = r.Pattern[i:] // without the method
		}
	})
}

// quiet requests come from machines, every few seconds: health checks and
// metric scrapes. Traced and logged, they'd drown out the real traffic.
func quiet(r *http.Request) bool {
	switch r.URL.Path {
	case "/healthz", "/readyz", "/metrics":
		return true
	}
	return false
}
