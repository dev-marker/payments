package wallet

import (
	"context"
	"fmt"

	walletv1 "github.com/fiston-user/payments/gen/wallet/v1"
	"github.com/fiston-user/payments/internal/platform/apperr"
	"github.com/fiston-user/payments/internal/platform/authn"
	"github.com/fiston-user/payments/internal/platform/idempotency"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GRPCServer serves the wallet API over gRPC, for our other services. Like
// Handler, it only translates: protobuf in, calls to the Service, protobuf
// out. The rules live in the Service, so both APIs share them.
type GRPCServer struct {
	walletv1.UnimplementedWalletServiceServer
	svc  *Service
	keys *idempotency.Store
}

// NewGRPCServer returns a GRPCServer that serves svc. Transfers run once per
// idempotency key, and keys keeps those.
func NewGRPCServer(svc *Service, keys *idempotency.Store) *GRPCServer {
	return &GRPCServer{svc: svc, keys: keys}
}

// parseID reads a wallet ID from a request field. Unlike a URL that doesn't
// exist, a malformed ID in a typed request is an invalid argument.
func parseID(field, id string) (uuid.UUID, error) {
	wid, err := uuid.Parse(id)
	if err != nil {
		var v apperr.ValidationError
		v.Add(field, "must be a UUID")
		return uuid.Nil, v.Err()
	}
	return wid, nil
}

func toMoney(m Money) *walletv1.Money {
	return &walletv1.Money{AmountMinor: m.Amount, Currency: m.Currency}
}

func fromMoney(m *walletv1.Money) Money {
	return Money{Amount: m.GetAmountMinor(), Currency: m.GetCurrency()}
}

// CreateWallet opens a wallet for the caller.
func (s *GRPCServer) CreateWallet(
	ctx context.Context, req *walletv1.CreateWalletRequest,
) (*walletv1.CreateWalletResponse, error) {
	acc, err := s.svc.CreateWallet(ctx, caller(ctx).String(), req.GetCurrency())
	if err != nil {
		return nil, err
	}
	return &walletv1.CreateWalletResponse{Wallet: walletPB(acc)}, nil
}

// GetWallet returns a wallet.
func (s *GRPCServer) GetWallet(
	ctx context.Context, req *walletv1.GetWalletRequest,
) (*walletv1.GetWalletResponse, error) {
	id, err := parseID("wallet_id", req.GetWalletId())
	if err != nil {
		return nil, err
	}
	acc, err := s.svc.As(caller(ctx)).Wallet(ctx, id)
	if err != nil {
		return nil, err
	}
	return &walletv1.GetWalletResponse{Wallet: walletPB(acc)}, nil
}

// walletPB converts a wallet to its protobuf message.
func walletPB(acc Account) *walletv1.Wallet {
	w := &walletv1.Wallet{
		Id:         acc.ID.String(),
		Currency:   acc.Currency,
		CreateTime: timestamppb.New(acc.CreatedAt),
	}
	if acc.OwnerID != nil {
		w.OwnerId = acc.OwnerID.String()
	}
	return w
}

// GetBalance returns a wallet's balance.
func (s *GRPCServer) GetBalance(
	ctx context.Context, req *walletv1.GetBalanceRequest,
) (*walletv1.GetBalanceResponse, error) {
	id, err := parseID("wallet_id", req.GetWalletId())
	if err != nil {
		return nil, err
	}
	b, err := s.svc.As(caller(ctx)).Balance(ctx, id)
	if err != nil {
		return nil, err
	}
	return &walletv1.GetBalanceResponse{
		WalletId: b.AccountID.String(),
		Balance:  toMoney(Money{Amount: b.Balance, Currency: b.Currency}),
	}, nil
}

// Deposit adds money to one of the caller's wallets, once per key.
func (s *GRPCServer) Deposit(
	ctx context.Context, req *walletv1.DepositRequest,
) (*walletv1.DepositResponse, error) {
	id, err := parseID("wallet_id", req.GetWalletId())
	if err != nil {
		return nil, err
	}
	amount := fromMoney(req.GetAmount())
	resp := &walletv1.DepositResponse{}
	err = s.once(ctx, req.GetIdempotencyKey(), req, resp, func(svc *Service) (proto.Message, error) {
		tx, err := svc.Deposit(ctx, id, amount)
		if err != nil {
			return nil, err
		}
		return &walletv1.DepositResponse{
			TransactionId: tx.ID.String(),
			WalletId:      id.String(),
			Amount:        toMoney(amount),
			CreateTime:    timestamppb.New(tx.CreatedAt),
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// Transfer moves money from one of the caller's wallets, once per key.
func (s *GRPCServer) Transfer(
	ctx context.Context, req *walletv1.TransferRequest,
) (*walletv1.TransferResponse, error) {
	amount := fromMoney(req.GetAmount())
	resp := &walletv1.TransferResponse{}
	err := s.once(ctx, req.GetIdempotencyKey(), req, resp, func(svc *Service) (proto.Message, error) {
		tr, err := svc.Transfer(ctx, req.GetFromWalletId(), req.GetToWalletId(), amount)
		if err != nil {
			return nil, err
		}
		return &walletv1.TransferResponse{
			TransactionId: tr.TransactionID.String(),
			FromWalletId:  tr.From.String(),
			ToWalletId:    tr.To.String(),
			Amount:        toMoney(tr.Amount),
			CreateTime:    timestamppb.New(tr.CreatedAt),
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// once runs do once per idempotency key, as the caller, and decodes the stored
// response into out: a first answer and a replay take the same path.
func (s *GRPCServer) once(
	ctx context.Context, value string, req, out proto.Message,
	do func(svc *Service) (proto.Message, error),
) error {
	key, err := requestKey(ctx, value, req)
	if err != nil {
		return err
	}
	stored, err := s.keys.Run(ctx, key, func(tx pgx.Tx) (idempotency.Response, error) {
		resp, err := do(s.svc.As(caller(ctx)).WithTx(tx))
		if err != nil {
			return idempotency.Response{}, err
		}
		body, err := proto.Marshal(resp)
		return idempotency.Response{Status: int(codes.OK), Body: body}, err
	})
	if err != nil {
		return err
	}
	if stored.Replayed {
		grpc.SetHeader(ctx, metadata.Pairs("idempotent-replayed", "true"))
	}
	if err := proto.Unmarshal(stored.Body, out); err != nil {
		return fmt.Errorf("decode stored response: %w", err)
	}
	return nil
}

// requestKey is an RPC's idempotency key, scoped to the user who sent it, with
// a fingerprint of the method and the whole request.
func requestKey(ctx context.Context, value string, req proto.Message) (idempotency.Key, error) {
	user, ok := authn.UserID(ctx)
	if !ok {
		return idempotency.Key{}, authn.ErrMissingToken
	}
	if value == "" || len(value) > 255 {
		var v apperr.ValidationError
		v.Add("idempotency_key", "must be 1 to 255 characters")
		return idempotency.Key{}, v.Err()
	}
	// Deterministic: the same request always encodes to the same bytes.
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		return idempotency.Key{}, err
	}
	method, _ := grpc.Method(ctx)
	fp, err := idempotency.Fingerprint("gRPC", method, body)
	if err != nil {
		return idempotency.Key{}, err
	}
	return idempotency.Key{Scope: user.String(), Value: value, Fingerprint: fp}, nil
}

// HoldServer serves the HoldService. It trusts the caller to say which user
// it acts for: users never reach it, and in episode 21, mTLS proves who calls.
type HoldServer struct {
	walletv1.UnimplementedHoldServiceServer
	svc *Service
}

// NewHoldServer returns a HoldServer that serves svc.
func NewHoldServer(svc *Service) *HoldServer {
	return &HoldServer{svc: svc}
}

// Hold sets money aside from the owner's wallet.
func (s *HoldServer) Hold(
	ctx context.Context, req *walletv1.HoldRequest,
) (*walletv1.HoldResponse, error) {
	id, err := parseID("hold_id", req.GetHoldId())
	if err != nil {
		return nil, err
	}
	owner, err := parseID("owner_id", req.GetOwnerId())
	if err != nil {
		return nil, err
	}
	wid, err := parseID("wallet_id", req.GetWalletId())
	if err != nil {
		return nil, err
	}
	h, err := s.svc.As(owner).Hold(ctx, id, wid, fromMoney(req.GetAmount()))
	if err != nil {
		return nil, err
	}
	return &walletv1.HoldResponse{Hold: holdPB(h)}, nil
}

// Capture pays a hold into another wallet.
func (s *HoldServer) Capture(
	ctx context.Context, req *walletv1.CaptureRequest,
) (*walletv1.CaptureResponse, error) {
	id, err := parseID("hold_id", req.GetHoldId())
	if err != nil {
		return nil, err
	}
	to, err := parseID("to_wallet_id", req.GetToWalletId())
	if err != nil {
		return nil, err
	}
	h, err := s.svc.Capture(ctx, id, to)
	if err != nil {
		return nil, err
	}
	return &walletv1.CaptureResponse{Hold: holdPB(h)}, nil
}

// Release gives a hold back to its wallet.
func (s *HoldServer) Release(
	ctx context.Context, req *walletv1.ReleaseRequest,
) (*walletv1.ReleaseResponse, error) {
	id, err := parseID("hold_id", req.GetHoldId())
	if err != nil {
		return nil, err
	}
	h, err := s.svc.Release(ctx, id)
	if err != nil {
		return nil, err
	}
	return &walletv1.ReleaseResponse{Hold: holdPB(h)}, nil
}

func holdPB(h Hold) *walletv1.Hold {
	return &walletv1.Hold{
		Id:       h.ID.String(),
		WalletId: h.WalletID.String(),
		Amount:   toMoney(Money{Amount: h.Amount, Currency: h.Currency}),
		Status:   string(h.Status),
	}
}
