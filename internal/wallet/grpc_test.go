package wallet_test

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"net"
	"slices"
	"testing"
	"time"

	walletv1 "github.com/fiston-user/payments/gen/wallet/v1"
	"github.com/fiston-user/payments/internal/auth"
	"github.com/fiston-user/payments/internal/platform/authn"
	"github.com/fiston-user/payments/internal/platform/grpcx"
	"github.com/fiston-user/payments/internal/platform/idempotency"
	"github.com/fiston-user/payments/internal/wallet"
	"github.com/fiston-user/payments/internal/wallet/walletclient"
	"github.com/google/uuid"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// grpcEnv is the wallet's gRPC API, served in memory, the way main wires it.
type grpcEnv struct {
	svc    *wallet.Service
	client *walletclient.Client
	health healthpb.HealthClient
	signer *auth.Signer // issues access tokens the server accepts
}

func newGRPC(t *testing.T) grpcEnv {
	t.Helper()
	pool := newPool(t)
	svc := wallet.NewService(wallet.NewStore(pool))
	pub, key, _ := ed25519.GenerateKey(nil)

	// The real server, with every interceptor, on a bufconn listener: an
	// in-memory network, so no ports and no flaky timing.
	srv := grpcx.NewServer(slog.New(slog.DiscardHandler),
		authn.NewVerifier(pub).UnaryInterceptor(healthpb.Health_ServiceDesc.ServiceName))
	walletv1.RegisterWalletServiceServer(srv, wallet.NewGRPCServer(svc, idempotency.NewStore(pool, nil)))
	healthpb.RegisterHealthServer(srv, health.NewServer())
	lis := bufconn.Listen(1 << 20)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	// The real client, dialing the listener instead of the network.
	dial := grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	})
	client, err := walletclient.New("passthrough:///bufconn", 5*time.Second, dial)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	// And a bare connection, without a token, the way a load balancer checks.
	conn, err := grpc.NewClient("passthrough:///bufconn", dial,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("health client: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return grpcEnv{svc, client, healthpb.NewHealthClient(conn), auth.NewSigner(key, time.Minute)}
}

// as returns a context that sends an access token for the owner of a wallet.
func (e grpcEnv) as(t *testing.T, wallet uuid.UUID) context.Context {
	t.Helper()
	w, err := e.svc.Wallet(t.Context(), wallet)
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}
	token, err := e.signer.Issue(*w.OwnerID, time.Now())
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return authn.WithToken(t.Context(), token)
}

func TestGRPCAuth(t *testing.T) {
	t.Parallel()
	env := newGRPC(t)
	mine, theirs := newWallet(t, env.svc, 0), newWallet(t, env.svc, 0)

	tests := []struct {
		name string
		ctx  context.Context
		code codes.Code
	}{
		{"no token", t.Context(), codes.Unauthenticated},
		{"not a token", authn.WithToken(t.Context(), "hello"), codes.Unauthenticated},
		{"valid token", env.as(t, mine), codes.OK},
		{"someone else's wallet", env.as(t, theirs), codes.NotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := env.client.GetBalance(tt.ctx, &walletv1.GetBalanceRequest{WalletId: mine.String()})
			if got := status.Code(err); got != tt.code {
				t.Errorf("code = %s, want %s (%v)", got, tt.code, err)
			}
		})
	}

	// Health checks are public: load balancers have no token.
	resp, err := env.health.Check(t.Context(), &healthpb.HealthCheckRequest{})
	if err != nil || resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("health = %v, %v; want SERVING", resp, err)
	}
}

func TestGRPCErrors(t *testing.T) {
	t.Parallel()
	env := newGRPC(t)
	alice, bob := newWallet(t, env.svc, 1000), newWallet(t, env.svc, 0)
	ctx, from, to := env.as(t, alice), alice.String(), bob.String()
	transfer := func(key string, amount int64) *walletv1.TransferRequest {
		return &walletv1.TransferRequest{IdempotencyKey: key, FromWalletId: from, ToWalletId: to,
			Amount: &walletv1.Money{AmountMinor: amount, Currency: "USD"}}
	}

	_, err := env.client.GetBalance(ctx, &walletv1.GetBalanceRequest{WalletId: uuid.NewString()})
	expect(t, "unknown wallet", err, codes.NotFound, "")
	_, err = env.client.GetBalance(ctx, &walletv1.GetBalanceRequest{WalletId: "42"})
	expect(t, "malformed ID", err, codes.InvalidArgument, "wallet_id")
	_, err = env.client.Transfer(ctx, transfer("", 100))
	expect(t, "no idempotency key", err, codes.InvalidArgument, "idempotency_key")
	_, err = env.client.Transfer(ctx, transfer(uuid.NewString(), 5000))
	expect(t, "too much", err, codes.FailedPrecondition, "insufficient_funds")

	// A retry with the same key replays the first response, and says so.
	key := uuid.NewString()
	first, err := env.client.Transfer(ctx, transfer(key, 600))
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	var header metadata.MD
	again, err := env.client.Transfer(ctx, transfer(key, 600), grpc.Header(&header))
	if err != nil || again.GetTransactionId() != first.GetTransactionId() {
		t.Fatalf("retry = %v, %v; want transaction %s", again, err, first.GetTransactionId())
	}
	if got := header.Get("idempotent-replayed"); !slices.Equal(got, []string{"true"}) {
		t.Errorf("idempotent-replayed = %v, want [true]", got)
	}
	_, err = env.client.Transfer(ctx, transfer(key, 700))
	expect(t, "key reused", err, codes.FailedPrecondition, "")
}

// expect checks that err is a status with the code, and with a detail that
// names detail: the field of a BadRequest, or the reason of an ErrorInfo.
func expect(t *testing.T, name string, err error, code codes.Code, detail string) {
	t.Helper()
	s := status.Convert(err)
	if s.Code() != code {
		t.Errorf("%s: code = %s, want %s (%v)", name, s.Code(), code, err)
		return
	}
	if detail == "" {
		return
	}
	for _, d := range s.Details() {
		switch d := d.(type) {
		case *errdetails.BadRequest:
			if d.GetFieldViolations()[0].GetField() == detail {
				return
			}
		case *errdetails.ErrorInfo:
			if d.GetReason() == detail {
				return
			}
		}
	}
	t.Errorf("%s: details = %v, want %q", name, s.Details(), detail)
}
