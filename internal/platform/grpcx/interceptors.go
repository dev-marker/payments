package grpcx

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/fiston-user/payments/internal/platform/httpx"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// requestIDKey carries request IDs between services: httpx.RequestIDHeader,
// as gRPC metadata, whose keys are always lowercase.
const requestIDKey = "x-request-id"

// NewServer returns a gRPC server with our interceptors, outermost first:
// request IDs, logging, panic recovery, error mapping, and then the service's
// own, like authentication.
func NewServer(logger *slog.Logger, interceptors ...grpc.UnaryServerInterceptor) *grpc.Server {
	chain := append([]grpc.UnaryServerInterceptor{
		RequestID, Log(logger), Recover(logger), Errors,
	}, interceptors...)
	// The stats handler traces every RPC, and runs before any interceptor, so
	// they all see the span.
	return grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(chain...),
	)
}

// RequestID gives every RPC a request ID: the one the caller sent, if it's
// valid, or a new one. Like httpx.RequestID, it puts the ID in the context,
// and on every log line, and sends it back in the response headers.
func RequestID(
	ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
) (any, error) {
	var sent string
	if v := metadata.ValueFromIncomingContext(ctx, requestIDKey); len(v) > 0 {
		sent = v[0]
	}
	ctx, id := httpx.WithRequestID(ctx, sent)
	grpc.SetHeader(ctx, metadata.Pairs(requestIDKey, id))
	return handler(ctx, req)
}

// Log logs one line per RPC when it completes: the method, the status code
// and the duration. Server faults are logged as errors.
func Log(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		code := status.Code(err)
		level := slog.LevelInfo
		if code == codes.Internal || code == codes.Unknown {
			level = slog.LevelError
		}
		logger.LogAttrs(ctx, level, "rpc",
			slog.String("method", info.FullMethod),
			slog.String("code", code.String()),
			slog.Duration("duration", time.Since(start)),
		)
		return resp, err
	}
}

// Recover turns a panic in a handler into an Internal error, and logs it with
// its stack trace. grpc-go doesn't recover panics: without this, one bad
// request takes the whole server down.
func Recover(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (resp any, err error) {
		defer func() {
			if v := recover(); v != nil {
				logger.ErrorContext(ctx, "panic recovered",
					"method", info.FullMethod,
					"panic", v,
					"stack", string(debug.Stack()),
				)
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

// Errors turns the errors that handlers return into statuses, with ToStatus.
// Internal errors are logged, and the client only sees a generic message.
func Errors(
	ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
) (any, error) {
	resp, err := handler(ctx, req)
	if err == nil {
		return resp, nil
	}
	s := ToStatus(err)
	if s.Code() == codes.Internal {
		slog.ErrorContext(ctx, "rpc failed", "err", err)
	}
	return nil, s.Err()
}

// Shutdown stops s gracefully: it stops taking new RPCs, and waits for the
// running ones to finish. If ctx ends first, it cancels them.
func Shutdown(ctx context.Context, s *grpc.Server) {
	done := make(chan struct{})
	go func() {
		s.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		s.Stop()
	}
}

// PropagateRequestID is a client interceptor that sends the request ID in ctx
// along with every RPC, so one ID follows a request across services.
func PropagateRequestID(
	ctx context.Context, method string, req, reply any,
	cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption,
) error {
	if id := httpx.RequestIDFrom(ctx); id != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, requestIDKey, id)
	}
	return invoker(ctx, method, req, reply, cc, opts...)
}
