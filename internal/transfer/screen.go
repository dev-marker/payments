package transfer

import (
	"context"
	"log/slog"
	"time"

	walletv1 "github.com/fiston-user/payments/gen/wallet/v1"
	"github.com/fiston-user/payments/internal/fx"
	"github.com/fiston-user/payments/internal/transfer/risk"
)

// A Screener decides whether a transfer may go ahead.
type Screener interface {
	Screen(ctx context.Context, t Transfer) (risk.Assessment, error)
}

// RiskScreener gathers a transfer's signals, then asks the risk engine.
type RiskScreener struct {
	engine   *risk.Engine
	velocity *risk.Velocity
	wallets  walletv1.HoldServiceClient
	store    *Store
	rates    fx.Provider
	logger   *slog.Logger
}

// NewRiskScreener returns a RiskScreener.
func NewRiskScreener(
	engine *risk.Engine, velocity *risk.Velocity, wallets walletv1.HoldServiceClient,
	store *Store, rates fx.Provider, logger *slog.Logger,
) *RiskScreener {
	return &RiskScreener{engine, velocity, wallets, store, rates, logger}
}

// Screen returns the risk engine's assessment of t.
func (s *RiskScreener) Screen(ctx context.Context, t Transfer) (risk.Assessment, error) {
	amount, err := s.inDollars(ctx, t)
	if err != nil {
		return risk.Assessment{}, err
	}
	resp, err := s.wallets.DescribeWallet(ctx, &walletv1.DescribeWalletRequest{WalletId: t.To.String()})
	if err != nil {
		return risk.Assessment{}, rejection(err) // no such wallet: rejected, before the hold
	}
	paid, err := s.store.PaidBefore(ctx, t.UserID, t.To)
	if err != nil {
		return risk.Assessment{}, err
	}
	signals := risk.Signals{
		Amount:         amount,
		ReceiverFrozen: resp.GetWallet().GetFrozen(),
		ReceiverAge:    time.Since(resp.GetWallet().GetCreateTime().AsTime()),
		PaidBefore:     paid,
	}
	signals.Recent, err = s.velocity.Record(ctx, t.UserID, t.ID, amount)
	if err != nil {
		// Never fail open with money, and never let a cache outage stop
		// every payment: without Redis, the rules send it to review.
		s.logger.WarnContext(ctx, "velocity unavailable", "transfer", t.ID, "err", err)
		signals.RecentUnknown = true
	}
	return s.engine.Evaluate(signals), nil
}

// inDollars is t's amount in US cents, rounded up: our limits are in dollars,
// and every currency is measured against the same ones.
func (s *RiskScreener) inDollars(ctx context.Context, t Transfer) (int64, error) {
	if t.Currency == "USD" {
		return t.Amount, nil
	}
	rate, err := s.rates.Rate(ctx, t.Currency, "USD")
	if err != nil {
		return 0, err
	}
	return fx.Convert(t.Amount, t.Currency, "USD", rate, fx.Up)
}
