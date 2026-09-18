package gateway

import (
	"net/http"
	"slices"

	"github.com/fiston-user/payments/internal/platform/httpx"
)

// cors lets browser apps from origins call the API, and answers preflights.
func cors(origins []string) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Add("Vary", "Origin")
			origin := r.Header.Get("Origin")
			allowed := origin != "" && slices.Contains(origins, origin)
			if allowed {
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Expose-Headers", "X-Request-ID, Retry-After, "+
					"X-RateLimit-Limit, X-RateLimit-Remaining, Idempotent-Replayed")
			}
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				if allowed {
					h.Set("Access-Control-Allow-Methods", "GET, POST")
					h.Set("Access-Control-Allow-Headers",
						"Authorization, Content-Type, Idempotency-Key, X-Request-ID")
					h.Set("Access-Control-Max-Age", "600") // the browser can skip preflights for ten minutes
				}
				// A preflight never reaches the routes, or Require.
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
