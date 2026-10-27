package transfer_test

import (
	"testing"
	"time"

	"github.com/fiston-user/payments/internal/transfer"
	"github.com/fiston-user/payments/internal/transfer/risk"
	"github.com/google/uuid"
)

func TestDenied(t *testing.T) {
	t.Parallel()
	s := newSaga(t)
	s.risk.decision = risk.Deny
	tr := s.finish(t, s.create(t).ID)

	if tr.State != transfer.Failed || tr.FailureReason != "risk_declined" {
		t.Errorf("transfer is %s %q, want failed %q", tr.State, tr.FailureReason, "risk_declined")
	}
	if n := s.wallet.calls["hold"]; n != 0 {
		t.Errorf("hold called %d times, want 0: nothing moves", n)
	}
}

func TestReview(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		verdict *transfer.Verdict // nil: nobody decides
		state   transfer.State
		reason  string
		hold    string
	}{
		{"approved", &transfer.Verdict{Approve: true, Reviewer: "dana", Note: "called Alice"},
			transfer.Completed, "", "captured"},
		{"rejected", &transfer.Verdict{Reviewer: "dana", Note: "Alice didn't send it"},
			transfer.Reversed, "review_rejected", "released"},
		{"nobody decides", nil, transfer.Reversed, "review_timed_out", "released"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newSaga(t)
			s.risk.decision = risk.Review
			s.store.ReviewTimeout = 200 * time.Millisecond

			// The money is held, and then the saga waits.
			id := s.create(t).ID
			if _, err := s.orch.RunOnce(t.Context()); err != nil {
				t.Fatalf("run: %v", err)
			}
			tr, err := s.store.Get(t.Context(), id)
			if hold := s.wallet.holds[id]; err != nil || tr.State != transfer.InReview || hold != "held" {
				t.Fatalf("transfer is %s, hold %q, %v; want in review, held", tr.State, hold, err)
			}

			if tt.verdict != nil {
				if _, err := s.store.Review(t.Context(), tr, *tt.verdict); err != nil {
					t.Fatalf("review: %v", err)
				}
				// A second verdict finds it decided.
				if _, err := s.store.Review(t.Context(), tr, *tt.verdict); err == nil {
					t.Error("second review: err = nil, want ErrNotInReview")
				}
			}
			tr = s.finish(t, tr.ID)
			if tr.State != tt.state || tr.FailureReason != tt.reason {
				t.Errorf("transfer is %s %q, want %s %q", tr.State, tr.FailureReason, tt.state, tt.reason)
			}
			if got := s.wallet.holds[tr.ID]; got != tt.hold {
				t.Errorf("hold is %q, want %q", got, tt.hold)
			}
		})
	}
}

// TestReviewAudited checks that a verdict leaves a row in the audit log: who
// decided, what, and about which transfer.
func TestReviewAudited(t *testing.T) {
	t.Parallel()
	s := newSaga(t)
	s.risk.decision = risk.Review
	tr := s.create(t)
	if _, err := s.orch.RunOnce(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	tr, err := s.store.Get(t.Context(), tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	verdict := transfer.Verdict{Approve: true, Reviewer: uuid.NewString(), Note: "called Alice"}
	if _, err := s.store.Review(t.Context(), tr, verdict); err != nil {
		t.Fatalf("review: %v", err)
	}
	var actor, action string
	err = s.pool.QueryRow(t.Context(), "SELECT actor, action FROM audit_log WHERE target = $1",
		tr.ID.String()).Scan(&actor, &action)
	if err != nil || actor != verdict.Reviewer || action != "transfer.approve" {
		t.Errorf("audit log: %q %q, %v; want %q transfer.approve", actor, action, err, verdict.Reviewer)
	}
}
