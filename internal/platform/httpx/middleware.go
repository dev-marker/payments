// Package httpx is the HTTP plumbing every service shares: middleware, JSON
// helpers, and one consistent way to turn errors into responses.
package httpx

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"time"
)

// Middleware wraps a handler to add behavior around every request.
type Middleware func(http.Handler) http.Handler

// Chain wraps h in mw. The first middleware is the outermost: it sees the
// request first, and the response last.
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for _, m := range slices.Backward(mw) {
		h = m(h)
	}
	return h
}

// statusRecorder is a ResponseWriter that remembers the status code and counts
// the bytes written, which the standard ResponseWriter doesn't expose.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK // Write without WriteHeader means 200
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the real ResponseWriter, for
// features like Flush that our wrapper doesn't implement itself.
func (w *statusRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// AccessLog logs one line per request when it completes: the method, path,
// status, duration and response size. Server errors are logged as errors.
func AccessLog(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if quiet(r) {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)

			if rec.status == 0 {
				rec.status = http.StatusOK // the handler wrote nothing at all
			}
			level := slog.LevelInfo
			if rec.status >= 500 {
				level = slog.LevelError
			}
			logger.LogAttrs(r.Context(), level, "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Duration("duration", time.Since(start)),
				slog.Int("bytes", rec.bytes),
			)
		})
	}
}

// Recover turns a panic in a handler into a 500 JSON error, and logs it with
// its stack trace.
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if v == http.ErrAbortHandler {
					panic(v) // a deliberate abort: let net/http handle it
				}
				logger.ErrorContext(r.Context(), "panic recovered",
					"panic", v,
					"stack", string(debug.Stack()),
				)
				writeError(w, r, http.StatusInternalServerError, errorDetail{
					Code:    "internal",
					Message: "internal server error",
				})
			}()
			next.ServeHTTP(w, r)
		})
	}
}
