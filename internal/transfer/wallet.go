package transfer

import (
	"context"

	walletv1 "github.com/fiston-user/payments/gen/wallet/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Wallet is what the saga needs from the wallet service. Every step is
// idempotent: a transfer's hold ID is its own ID.
type Wallet interface {
	Hold(ctx context.Context, t Transfer) error
	Capture(ctx context.Context, t Transfer) error
	Release(ctx context.Context, t Transfer) error
}

// A Rejection is a step the wallet refused for good, like insufficient
// funds. Any other error might go away.
type Rejection struct {
	Reason string
}

func (r *Rejection) Error() string {
	return "rejected: " + r.Reason
}

// NewWallet returns the wallet service's HoldService, as a Wallet.
func NewWallet(client walletv1.HoldServiceClient) Wallet {
	return holds{client}
}

type holds struct {
	client walletv1.HoldServiceClient
}

func (h holds) Hold(ctx context.Context, t Transfer) error {
	_, err := h.client.Hold(ctx, &walletv1.HoldRequest{
		HoldId:   t.ID.String(),
		OwnerId:  t.UserID.String(),
		WalletId: t.From.String(),
		Amount:   &walletv1.Money{AmountMinor: t.Amount, Currency: t.Currency},
	})
	return rejection(err)
}

func (h holds) Capture(ctx context.Context, t Transfer) error {
	_, err := h.client.Capture(ctx, &walletv1.CaptureRequest{
		HoldId:     t.ID.String(),
		ToWalletId: t.To.String(),
	})
	return rejection(err)
}

func (h holds) Release(ctx context.Context, t Transfer) error {
	_, err := h.client.Release(ctx, &walletv1.ReleaseRequest{HoldId: t.ID.String()})
	return rejection(err)
}

// rejection turns the wallet's final answers into a Rejection.
func rejection(err error) error {
	s := status.Convert(err)
	switch s.Code() {
	case codes.FailedPrecondition:
		for _, d := range s.Details() {
			if info, ok := d.(*errdetails.ErrorInfo); ok {
				return &Rejection{Reason: info.GetReason()}
			}
		}
		return &Rejection{Reason: "failed_precondition"}
	case codes.NotFound:
		return &Rejection{Reason: "wallet_not_found"}
	case codes.InvalidArgument:
		return &Rejection{Reason: "invalid_request"}
	}
	return err
}
