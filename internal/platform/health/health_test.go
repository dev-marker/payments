package health_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fiston-user/payments/internal/platform/health"
)

func TestProbes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		err   error // what the database check returns
		drain bool
		ready int // /readyz's status: /healthz must always be 200
	}{
		{"ready", nil, false, http.StatusOK},
		{"database down", errors.New("connection refused"), false, http.StatusServiceUnavailable},
		{"draining", nil, true, http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := health.New(slog.New(slog.DiscardHandler), map[string]health.Check{
				"postgres": func(context.Context) error { return tt.err },
			})
			if tt.drain {
				p.Drain(0)
			}
			mux := http.NewServeMux()
			p.Register(mux)
			for path, want := range map[string]int{"/healthz": http.StatusOK, "/readyz": tt.ready} {
				rr := httptest.NewRecorder()
				mux.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
				if rr.Code != want {
					t.Errorf("%s: status %d, want %d", path, rr.Code, want)
				}
			}
		})
	}
}
