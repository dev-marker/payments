package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fiston-user/payments/internal/platform/apperr"
	"github.com/fiston-user/payments/internal/platform/audit"
	"github.com/fiston-user/payments/internal/platform/events"
	"github.com/fiston-user/payments/internal/platform/telemetry"
	"github.com/fiston-user/payments/internal/transfer/risk"
	"github.com/fiston-user/payments/internal/transfer/transferdb"
	"github.com/fiston-user/payments/internal/transfer/transferevents"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store keeps transfers, their history and their events.
type Store struct {
	pool *pgxpool.Pool
	q    *transferdb.Queries
	// ReviewTimeout is how long a transfer waits in review before it's
	// rejected: nobody's money stays held forever.
	ReviewTimeout time.Duration
}

// NewStore returns a Store on pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: transferdb.New(pool), ReviewTimeout: 24 * time.Hour}
}

// Create stores a pending transfer, once per idempotency key. If the key was
// used before, it returns that transfer, and false.
func (s *Store) Create(ctx context.Context, nt NewTransfer) (Transfer, bool, error) {
	// The saga runs later, in steps that continue this request's trace.
	nt.TraceContext = telemetry.Carry(ctx)
	row, err := s.q.CreateTransfer(ctx, nt)
	if err == nil {
		return fromRow(row), true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Transfer{}, false, fmt.Errorf("create transfer: %w", err)
	}
	row, err = s.q.GetTransferByKey(ctx, transferdb.GetTransferByKeyParams{
		UserID:         nt.UserID,
		IdempotencyKey: nt.IdempotencyKey,
	})
	if err != nil {
		return Transfer{}, false, fmt.Errorf("get transfer by key: %w", err)
	}
	return fromRow(row), false, nil
}

// Get returns the transfer with the given ID.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (Transfer, error) {
	row, err := s.q.GetTransfer(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Transfer{}, ErrTransferNotFound
	}
	if err != nil {
		return Transfer{}, fmt.Errorf("get transfer %s: %w", id, err)
	}
	return fromRow(row), nil
}

// Claim leases up to n due transfers to the caller, until lease runs out.
func (s *Store) Claim(ctx context.Context, n int, lease time.Duration) ([]Transfer, error) {
	rows, err := s.q.ClaimTransfers(ctx, transferdb.ClaimTransfersParams{
		LeaseUntil: time.Now().Add(lease),
		BatchSize:  int32(n),
	})
	if err != nil {
		return nil, fmt.Errorf("claim transfers: %w", err)
	}
	claimed := make([]Transfer, len(rows))
	for i, r := range rows {
		claimed[i] = fromRow(r)
	}
	return claimed, nil
}

// A Step is one try of a saga step, for the history.
type Step struct {
	Name    string // hold, capture or release
	Outcome string // succeeded, rejected or failed
	Detail  string // why it was rejected, or the error
}

// Advance moves t to next, with its history, and an event if the saga is
// over or waits for a person, in one transaction. If t changed since we read
// it: ErrStale.
func (s *Store) Advance(ctx context.Context, t Transfer, next State, step Step) (Transfer, error) {
	return s.advance(ctx, t, next, step, nil)
}

// advance is Advance, and runs extra too, if there is one, in the same transaction.
func (s *Store) advance(
	ctx context.Context, t Transfer, next State, step Step, extra func(pgx.Tx) error,
) (Transfer, error) {
	if !t.State.CanBecome(next) {
		return Transfer{}, fmt.Errorf("transfer %s: %s can't become %s", t.ID, t.State, next)
	}
	// In review, it's due again when the review times out.
	var wait time.Duration
	if next == InReview {
		wait = s.ReviewTimeout
	}
	var moved Transfer
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		row, err := q.AdvanceTransfer(ctx, transferdb.AdvanceTransferParams{
			ID:            t.ID,
			State:         string(t.State),
			Version:       t.version,
			NextState:     string(next),
			FailureReason: t.FailureReason,
			WaitSeconds:   wait.Seconds(),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStale
		}
		if err != nil {
			return err
		}
		moved = fromRow(row)
		if err := insertStep(ctx, q, t.ID, step); err != nil {
			return err
		}
		if extra != nil {
			if err := extra(tx); err != nil {
				return err
			}
		}
		return enqueue(ctx, q, moved)
	})
	if err != nil {
		return Transfer{}, fmt.Errorf("advance transfer %s: %w", t.ID, err)
	}
	return moved, nil
}

// Retry records a failed step, and schedules another try at retryAt.
func (s *Store) Retry(ctx context.Context, t Transfer, step Step, retryAt time.Time) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		n, err := q.RetryTransfer(ctx, transferdb.RetryTransferParams{
			ID:      t.ID,
			Version: t.version,
			RetryAt: retryAt,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrStale
		}
		return insertStep(ctx, q, t.ID, step)
	})
	if err != nil {
		return fmt.Errorf("retry transfer %s: %w", t.ID, err)
	}
	return nil
}

func insertStep(ctx context.Context, q *transferdb.Queries, id uuid.UUID, step Step) error {
	return q.InsertStep(ctx, transferdb.InsertStepParams{
		TransferID: id,
		Step:       step.Name,
		Outcome:    step.Outcome,
		Detail:     step.Detail,
	})
}

// enqueue writes t's event to the outbox: when its saga is over, or when it
// waits for a person. The other states have none.
func enqueue(ctx context.Context, q *transferdb.Queries, t Transfer) error {
	var typ string
	switch t.State {
	case Completed:
		typ = transferevents.Completed
	case Failed, Reversed:
		typ = transferevents.Failed
	case InReview:
		typ = transferevents.ReviewRequired
	default:
		return nil
	}
	evt, err := events.New(typ, t.ID, transferevents.Transfer{
		ID:            t.ID,
		UserID:        t.UserID,
		FromWalletID:  t.From,
		ToWalletID:    t.To,
		Amount:        t.Amount,
		Currency:      t.Currency,
		State:         string(t.State),
		FailureReason: t.FailureReason,
	})
	if err != nil {
		return err
	}
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("encode event %s: %w", typ, err)
	}
	return q.InsertOutboxEvent(ctx, transferdb.InsertOutboxEventParams{
		EventID: evt.ID,
		Subject: transferevents.Subject(typ),
		Payload: payload,
		// The trace of the step that finished the saga.
		TraceContext: telemetry.Carry(ctx),
	})
}

// Decide records the risk engine's assessment of transfer id.
func (s *Store) Decide(ctx context.Context, id uuid.UUID, a risk.Assessment) error {
	return s.q.InsertDecision(ctx, transferdb.InsertDecisionParams{
		TransferID: id,
		Decision:   a.Decision.String(),
		Reasons:    a.Reasons,
		DecidedBy:  "risk engine",
	})
}

// PaidBefore reports whether user has ever paid wallet.
func (s *Store) PaidBefore(ctx context.Context, user, wallet uuid.UUID) (bool, error) {
	paid, err := s.q.PaidBefore(ctx, transferdb.PaidBeforeParams{UserID: user, ToWalletID: wallet})
	if err != nil {
		return false, fmt.Errorf("paid before: %w", err)
	}
	return paid, nil
}

// A Verdict is a person's decision on a transfer in review.
type Verdict struct {
	Approve  bool
	Reviewer string
	Note     string // why, for whoever reads it next
}

// ErrNotInReview means the transfer isn't waiting for a decision: it never
// was, or someone, or the timeout, decided first.
var ErrNotInReview = fmt.Errorf("%w: transfer is not in review", apperr.ErrConflict)

// Review carries out v on t: approved, on to the capture; rejected, back to
// the sender. The verdict is recorded in the same transaction.
func (s *Store) Review(ctx context.Context, t Transfer, v Verdict) (Transfer, error) {
	if t.State != InReview {
		return Transfer{}, ErrNotInReview
	}
	next, outcome, decision := FundsHeld, "succeeded", "approve"
	if !v.Approve {
		next, outcome, decision = Compensating, "rejected", "reject"
		t.FailureReason = "review_rejected"
	}
	step := Step{"review", outcome, v.Note}
	moved, err := s.advance(ctx, t, next, step, func(tx pgx.Tx) error {
		err := s.q.WithTx(tx).InsertDecision(ctx, transferdb.InsertDecisionParams{
			TransferID: t.ID,
			Decision:   decision,
			DecidedBy:  v.Reviewer,
			Note:       v.Note,
		})
		if err != nil {
			return err
		}
		// And in the audit log: who decided, and about how much money.
		return audit.Record(ctx, tx, audit.Event{
			Actor:  v.Reviewer,
			Action: "transfer." + decision,
			Target: t.ID.String(),
			Details: map[string]any{
				"note": v.Note, "amount": t.Amount, "currency": t.Currency, "user_id": t.UserID,
			},
		})
	})
	if errors.Is(err, ErrStale) {
		return Transfer{}, ErrNotInReview
	}
	return moved, err
}
