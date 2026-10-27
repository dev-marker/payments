package authn

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// UnaryInterceptor is Require, for gRPC. It only lets RPCs with a valid access
// token through, sent as authorization: Bearer <token> in the metadata. RPCs
// to the services named in public, like health checks, need no token.
func (v *Verifier) UnaryInterceptor(public ...string) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (any, error) {
		// FullMethod looks like /wallet.v1.WalletService/GetBalance.
		service, _, _ := strings.Cut(strings.TrimPrefix(info.FullMethod, "/"), "/")
		if slices.Contains(public, service) {
			return handler(ctx, req)
		}
		values := metadata.ValueFromIncomingContext(ctx, "authorization")
		if len(values) != 1 || bearer(values[0]) == "" {
			return nil, ErrMissingToken
		}
		user, err := v.Verify(bearer(values[0]))
		if err != nil {
			slog.InfoContext(ctx, "access token refused", "err", err)
			return nil, ErrInvalidToken
		}
		return handler(withUser(ctx, user), req)
	}
}

// WithToken returns a copy of ctx that sends token with every gRPC call made
// with it: the client's half of UnaryInterceptor.
func WithToken(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}
