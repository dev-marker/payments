package gateway_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/fiston-user/payments/internal/gateway"
)

func TestProxy(t *testing.T) {
	t.Parallel()
	// A fake auth service, which answers with the headers it got.
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Got-Request-ID", r.Header.Get("X-Request-ID"))
		w.Header().Set("Got-Forwarded-For", r.Header.Get("X-Forwarded-For"))
	}))
	t.Cleanup(auth.Close)
	authURL, _ := url.Parse(auth.URL)
	gw := gateway.New(gateway.Config{AuthURL: authURL, Redis: testRedis(t), IPLimit: 10,
		UserLimit: 10, Logger: slog.New(slog.DiscardHandler)})

	req := httptest.NewRequest("POST", "/v1/auth/login", nil)
	req.RemoteAddr = "203.0.113.7:51234"
	req.Header.Set("X-Request-ID", "req-42")
	req.Header.Set("X-Forwarded-For", "6.6.6.6") // made up by the client
	rr := httptest.NewRecorder()
	gw.ServeHTTP(rr, req)

	// Auth gets our request ID and the real address. The client gets one ID back.
	got := rr.Header()
	if got.Get("Got-Request-ID") != "req-42" || got.Get("Got-Forwarded-For") != "203.0.113.7" {
		t.Errorf("auth got request ID %q from %q, want req-42 from 203.0.113.7",
			got.Get("Got-Request-ID"), got.Get("Got-Forwarded-For"))
	}
	if ids := got.Values("X-Request-ID"); len(ids) != 1 {
		t.Errorf("X-Request-ID = %v, want exactly one", ids)
	}
}
