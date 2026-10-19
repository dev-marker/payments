package transfer_test

import (
	"testing"

	"github.com/fiston-user/payments/internal/transfer"
)

func TestTransitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from, to transfer.State
		ok       bool
	}{
		{transfer.Pending, transfer.FundsHeld, true},
		{transfer.Pending, transfer.Failed, true},
		{transfer.Pending, transfer.Completed, false}, // nothing arrives without a hold
		{transfer.Pending, transfer.InReview, true},
		{transfer.InReview, transfer.FundsHeld, true},    // approved
		{transfer.InReview, transfer.Compensating, true}, // rejected, or timed out
		{transfer.InReview, transfer.Completed, false},   // not without the capture
		{transfer.FundsHeld, transfer.Completed, true},
		{transfer.FundsHeld, transfer.Compensating, true},
		{transfer.FundsHeld, transfer.Failed, false}, // not with the money still held
		{transfer.Compensating, transfer.Reversed, true},
		{transfer.Compensating, transfer.Completed, false}, // no changing our mind
		{transfer.Completed, transfer.Reversed, false},     // finished is finished
		{transfer.Failed, transfer.Pending, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.from)+" to "+string(tt.to), func(t *testing.T) {
			if got := tt.from.CanBecome(tt.to); got != tt.ok {
				t.Errorf("CanBecome = %v, want %v", got, tt.ok)
			}
		})
	}
}
