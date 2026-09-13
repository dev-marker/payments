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
		v.Add(field, "must be a wallet ID")
		return uuid.Nil, v.Err()
	}
	return wid, nil
}

func toMoney(m Money) *walletv1.Money {
	return &walletv1.Money{AmountMinor: m.Amount, Currency: m.Currency}
}

// GetWallet returns a wallet.
func (s *GRPCServer) GetWallet(
	ctx context.Context, req *walletv1.GetWalletRequest,
) (*walletv1.GetWalletResponse, error) {
	id, err := parseID("wallet_id", req.GetWalletId())
	if err != nil {
		return nil, err
	}
	acc, err := s.svc.Wallet(ctx, id)
	if err != nil {
		return nil, err
	}
	w := &walletv1.Wallet{
		Id:         acc.ID.String(),
		Currency:   acc.Currency,
		CreateTime: timestamppb.New(acc.CreatedAt),
	}
	if acc.OwnerID != nil {
		w.OwnerId = acc.OwnerID.String()
	}
	return &walletv1.GetWalletResponse{Wallet: w}, nil
}

// GetBalance returns a wallet's balance.
func (s *GRPCServer) GetBalance(
	ctx context.Context, req *walletv1.GetBalanceRequest,
) (*walletv1.GetBalanceResponse, error) {
	id, err := parseID("wallet_id", req.GetWalletId())
	if err != nil {
		return nil, err
	}
	b, err := s.svc.Balance(ctx, id)
	if err != nil {
		return nil, err
	}
	return &walletv1.GetBalanceResponse{
		WalletId: b.AccountID.String(),
		Balance:  toMoney(Money{Amount: b.Balance, Currency: b.Currency}),
	}, nil
}

// Transfer moves money between two wallets, once per idempotency key.
func (s *GRPCServer) Transfer(
	ctx context.Context, req *walletv1.TransferRequest,
) (*walletv1.TransferResponse, error) {
	key, err := transferKey(ctx, req)
	if err != nil {
		return nil, err
	}
	amount := Money{Amount: req.GetAmount().GetAmountMinor(), Currency: req.GetAmount().GetCurrency()}
	stored, err := s.keys.Run(ctx, key, func(tx pgx.Tx) (idempotency.Response, error) {
		tr, err := s.svc.WithTx(tx).Transfer(ctx, req.GetFromWalletId(), req.GetToWalletId(), amount)
		if err != nil {
			return idempotency.Response{}, err
		}
		// We store the response in protobuf's own encoding, with the gRPC
		// status code, OK, where HTTP keeps its 201.
		body, err := proto.Marshal(&walletv1.TransferResponse{
			TransactionId: tr.TransactionID.String(),
			FromWalletId:  tr.From.String(),
			ToWalletId:    tr.To.String(),
			Amount:        toMoney(tr.Amount),
			CreateTime:    timestamppb.New(tr.CreatedAt),
		})
		return idempotency.Response{Status: int(codes.OK), Body: body}, err
	})
	if err != nil {
		return nil, err
	}
	if stored.Replayed {
		grpc.SetHeader(ctx, metadata.Pairs("idempotent-replayed", "true"))
	}
	// A first answer and a replay take the same path: decode what we stored.
	resp := &walletv1.TransferResponse{}
	if err := proto.Unmarshal(stored.Body, resp); err != nil {
		return nil, fmt.Errorf("decode stored response: %w", err)
	}
	return resp, nil
}

// transferKey is a transfer's idempotency key: the key in the request, scoped
// to the user who sent it, with a fingerprint of the whole request.
func transferKey(ctx context.Context, req *walletv1.TransferRequest) (idempotency.Key, error) {
	user, ok := authn.UserID(ctx)
	if !ok {
		return idempotency.Key{}, authn.ErrMissingToken
	}
	if k := req.GetIdempotencyKey(); k == "" || len(k) > 255 {
		var v apperr.ValidationError
		v.Add("idempotency_key", "must be 1 to 255 characters")
		return idempotency.Key{}, v.Err()
	}
	// Deterministic: the same request always encodes to the same bytes.
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		return idempotency.Key{}, err
	}
	fp, err := idempotency.Fingerprint("gRPC", walletv1.WalletService_Transfer_FullMethodName, body)
	if err != nil {
		return idempotency.Key{}, err
	}
	return idempotency.Key{Scope: user.String(), Value: req.GetIdempotencyKey(), Fingerprint: fp}, nil
}
