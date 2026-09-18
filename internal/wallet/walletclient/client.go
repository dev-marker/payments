// Package walletclient calls the wallet service over gRPC. Other services use
// it instead of the bare generated client, so every caller gets deadlines,
// retries and request IDs right, in one place.
package walletclient

import (
	"context"
	"fmt"
	"time"

	walletv1 "github.com/fiston-user/payments/gen/wallet/v1"
	"github.com/fiston-user/payments/internal/platform/grpcx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// serviceConfig is how gRPC calls the wallet: spread over all its addresses,
// and with automatic retries, but only where a retry can't do harm. Reads are
// always safe. Transfer is safe only because a retry sends the same message,
// with the same idempotency key. ABORTED is a transfer that's still running.
const serviceConfig = `{
	"loadBalancingConfig": [{"round_robin": {}}],
	"methodConfig": [{
		"name": [
			{"service": "wallet.v1.WalletService", "method": "GetWallet"},
			{"service": "wallet.v1.WalletService", "method": "GetBalance"}
		],
		"retryPolicy": {
			"maxAttempts": 3,
			"initialBackoff": "0.1s",
			"maxBackoff": "1s",
			"backoffMultiplier": 2,
			"retryableStatusCodes": ["UNAVAILABLE"]
		}
	}, {
		"name": [
			{"service": "wallet.v1.WalletService", "method": "Deposit"},
			{"service": "wallet.v1.WalletService", "method": "Transfer"}
		],
		"retryPolicy": {
			"maxAttempts": 3,
			"initialBackoff": "0.2s",
			"maxBackoff": "2s",
			"backoffMultiplier": 2,
			"retryableStatusCodes": ["UNAVAILABLE", "ABORTED"]
		}
	}],
	"retryThrottling": {"maxTokens": 10, "tokenRatio": 0.1}
}`

// Client calls the wallet service. It's safe for concurrent use: make one when
// your service starts, and share it.
type Client struct {
	walletv1.WalletServiceClient
	conn *grpc.ClientConn
}

// New returns a Client for the wallet service at target, like
// dns:///wallet:9081. Every call gets a deadline of at most timeout, or an
// earlier one from its context. opts are added to ours, for tests.
func New(target string, timeout time.Duration, opts ...grpc.DialOption) (*Client, error) {
	opts = append([]grpc.DialOption{
		// Plain text inside our network, for now. Episode 21 adds mTLS.
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultServiceConfig(serviceConfig),
		grpc.WithChainUnaryInterceptor(grpcx.PropagateRequestID, deadline(timeout)),
	}, opts...)
	// NewClient doesn't connect yet: the first call does, and gRPC
	// reconnects by itself whenever it has to.
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, fmt.Errorf("wallet client: %w", err)
	}
	return &Client{WalletServiceClient: walletv1.NewWalletServiceClient(conn), conn: conn}, nil
}

// Close closes the connection to the wallet service.
func (c *Client) Close() error {
	return c.conn.Close()
}

// deadline gives every call a deadline, at most d from now. When the caller's
// context ends sooner, that deadline wins: a child context never outlives its
// parent. So a call made for a request never outlasts the request.
func deadline(d time.Duration) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context, method string, req, reply any,
		cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption,
	) error {
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
