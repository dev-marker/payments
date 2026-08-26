package httpx_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/fiston-user/payments/internal/platform/logging"
)

func TestRequestID(t *testing.T) {
	tests := []struct {
		name, header string
		keep         bool
	}{
		{"generated when missing", "", false},
		{"kept when valid", "checkout-7f3a", true},
		{"replaced when invalid", "bad id\nlevel=ERROR", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen string
			h := httpx.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = httpx.RequestIDFrom(r.Context())
			}))
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set(httpx.RequestIDHeader, tt.header)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			got := rr.Header().Get(httpx.RequestIDHeader)
			if got == "" || got != seen {
				t.Fatalf("response ID %q, handler saw %q: want the same, non-empty ID", got, seen)
			}
			if (got == tt.header) != tt.keep {
				t.Errorf("ID = %q for header %q, keep = %v", got, tt.header, tt.keep)
			}
		})
	}
}

// newLogger returns a JSON logger that writes into buf, so tests can read it.
func newLogger(buf *bytes.Buffer) *slog.Logger {
	return logging.New(buf, logging.Options{Service: "test", Format: "json"})
}

func TestAccessLog(t *testing.T) {
	var buf bytes.Buffer
	h := httpx.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("short and stout"))
	}), httpx.RequestID, httpx.AccessLog(newLogger(&buf)))

	req := httptest.NewRequest("GET", "/teapot", nil)
	req.Header.Set(httpx.RequestIDHeader, "req-1")
	h.ServeHTTP(httptest.NewRecorder(), req)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("want one JSON log line, got %q: %v", buf.String(), err)
	}
	want := map[string]any{
		"msg": "request", "service": "test", "request_id": "req-1",
		"method": "GET", "path": "/teapot", "status": 418.0, "bytes": 15.0,
	}
	for k, v := range want {
		if line[k] != v {
			t.Errorf("log %s = %v, want %v", k, line[k], v)
		}
	}
}

func TestRecover(t *testing.T) {
	var buf bytes.Buffer
	h := httpx.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("something broke")
	}), httpx.RequestID, httpx.Recover(newLogger(&buf)))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
	body := decodeError(t, rr)
	if body.Error.Code != "internal" || body.Error.RequestID == "" {
		t.Errorf("got code %q and request ID %q", body.Error.Code, body.Error.RequestID)
	}
	if !strings.Contains(buf.String(), "something broke") {
		t.Errorf("panic not logged: %s", buf.String())
	}
}

func TestChainOrder(t *testing.T) {
	var order []string
	mark := func(name string) httpx.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := httpx.Chain(http.NotFoundHandler(), mark("first"), mark("second"), mark("third"))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))

	if want := []string{"first", "second", "third"}; !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}
