package mtls

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/fiston-user/payments/internal/platform/apperr"
	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/fiston-user/payments/internal/platform/logging"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// Peer returns the name of the service at the other end of a connection: the
// one name in the certificate it showed, which our CA vouched for. It's ""
// when there's no verified certificate.
func Peer(cs *tls.ConnectionState) string {
	if cs == nil || len(cs.VerifiedChains) == 0 || len(cs.VerifiedChains[0][0].DNSNames) != 1 {
		return ""
	}
	return cs.VerifiedChains[0][0].DNSNames[0]
}

type peerKey struct{}

// withPeer returns a copy of ctx that carries the calling service's name, for
// PeerFrom, and for every line logged with it.
func withPeer(ctx context.Context, name string) context.Context {
	ctx = context.WithValue(ctx, peerKey{}, name)
	return logging.With(ctx, slog.String("peer", name))
}

// PeerFrom returns the name of the service that made the request, as Allow or
// Authorize stored it in ctx.
func PeerFrom(ctx context.Context) string {
	name, _ := ctx.Value(peerKey{}).(string)
	return name
}

// Allow is middleware that only lets the named services through. Any other
// caller gets a 403, even with a certificate from our CA, and so does a
// request that didn't come over mTLS at all.
func Allow(names ...string) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := Peer(r.TLS)
			if !slices.Contains(names, name) {
				// Misconfigured, or someone probing: either way, worth an alert.
				slog.WarnContext(r.Context(), "caller refused", "peer", name, "path", r.URL.Path)
				httpx.Error(w, r, fmt.Errorf("%w: %q may not call this", apperr.ErrForbidden, name))
				return
			}
			next.ServeHTTP(w, r.WithContext(withPeer(r.Context(), name)))
		})
	}
}

// Authorize is Allow, for gRPC. callers lists who may call each service, by
// its full name, like wallet.v1.HoldService: a service that isn't listed
// can't be called at all.
func Authorize(callers map[string][]string) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (any, error) {
		var name string
		if p, ok := peer.FromContext(ctx); ok {
			if t, ok := p.AuthInfo.(credentials.TLSInfo); ok {
				name = Peer(&t.State)
			}
		}
		service, _, _ := strings.Cut(strings.TrimPrefix(info.FullMethod, "/"), "/")
		if !slices.Contains(callers[service], name) {
			slog.WarnContext(ctx, "caller refused", "peer", name, "method", info.FullMethod)
			return nil, fmt.Errorf("%w: %q may not call %s", apperr.ErrForbidden, name, service)
		}
		return handler(withPeer(ctx, name), req)
	}
}
