package httpx

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"

	"github.com/fiston-user/payments/internal/platform/logging"
)

// RequestIDHeader carries a request's ID between services, and back to clients.
const RequestIDHeader = "X-Request-ID"

type requestIDKey struct{}

// RequestID gives every request an ID. It keeps a valid ID sent by the caller,
// so one ID can follow a request across services, and makes a new one
// otherwise. The ID goes into the context, onto every log line, and back to
// the client in the response header.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validRequestID(id) {
			id = rand.Text()
		}
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		ctx = logging.With(ctx, slog.String("request_id", id))
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFrom returns the ID of the request that ctx belongs to, or "".
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// validRequestID accepts up to 64 letters, digits, dashes and underscores. The
// header comes from the client, and we copy it into our logs and responses.
func validRequestID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
