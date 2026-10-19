package risk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/fiston-user/payments/internal/transfer/risk"
)

func TestEngine(t *testing.T) {
	t.Parallel()
	engine := risk.NewEngine(risk.Rules(risk.Limits{
		MaxCount:       5,
		MaxAmount:      200_000,
		ReviewAbove:    100_000,
		FirstTimeAbove: 25_000,
		NewWalletAge:   24 * time.Hour,
	})...)
	// Each case changes an ordinary transfer: fifteen dollars, the first one
	// today, to a wallet that's a year old, and that we've paid before.
	tests := []struct {
		name   string
		change func(s *risk.Signals)
		want   string // the decision, then the reasons
	}{
		{"ordinary", func(s *risk.Signals) {}, "allow"},
		{"receiver frozen", func(s *risk.Signals) { s.ReceiverFrozen = true }, "deny receiver_frozen"},
		{"fifth in ten minutes", func(s *risk.Signals) { s.Recent.Count = 5 }, "allow"},
		{"sixth in ten minutes", func(s *risk.Signals) { s.Recent.Count = 6 }, "deny velocity_count"},
		{"$2,000.01 today", func(s *risk.Signals) { s.Recent.Amount = 200_001 }, "review velocity_amount"},
		{"redis down", func(s *risk.Signals) { s.RecentUnknown = true }, "review velocity_unknown"},
		{"$1,000", func(s *risk.Signals) { s.Amount = 100_000 }, "review large_amount"},
		{"$999.99", func(s *risk.Signals) { s.Amount = 99_999 }, "allow"},
		{"$300 to a new wallet", func(s *risk.Signals) { s.Amount, s.ReceiverAge = 30_000, time.Hour },
			"review new_wallet"},
		{"$15 to a new wallet", func(s *risk.Signals) { s.ReceiverAge = time.Hour }, "allow"},
		{"$300 to someone new", func(s *risk.Signals) { s.Amount, s.PaidBefore = 30_000, false },
			"review new_recipient"},
		{"deny wins, reasons kept", func(s *risk.Signals) { s.ReceiverFrozen, s.Amount = true, 150_000 },
			"deny receiver_frozen large_amount"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := risk.Signals{
				Amount:      1500,
				Recent:      risk.Recent{Count: 1, Amount: 1500},
				ReceiverAge: 365 * 24 * time.Hour,
				PaidBefore:  true,
			}
			tt.change(&s)
			a := engine.Evaluate(s)
			got := strings.Join(append([]string{a.Decision.String()}, a.Reasons...), " ")
			if got != tt.want {
				t.Errorf("Evaluate = %q, want %q", got, tt.want)
			}
		})
	}
}
