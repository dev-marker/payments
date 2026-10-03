package transfer

import (
	"errors"
	"fmt"
	"time"

	"github.com/fiston-user/payments/internal/platform/apperr"
	"github.com/fiston-user/payments/internal/transfer/transferdb"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/propagation"
)

// Transfer is money moving between two wallets, and where it's at.
type Transfer struct {
	ID            uuid.UUID `json:"id"`
	UserID        uuid.UUID `json:"-"`
	From          uuid.UUID `json:"from"`
	To            uuid.UUID `json:"to"`
	Amount        int64     `json:"amount"`
	Currency      string    `json:"currency"`
	State         State     `json:"state"`
	FailureReason string    `json:"failure_reason,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`

	attempts     int32                  // tries of the current step
	version      int32                  // the row's version, as we read it
	traceContext propagation.MapCarrier // the trace of the request that created it
}

func fromRow(r transferdb.Transfer) Transfer {
	return Transfer{
		ID: r.ID, UserID: r.UserID, From: r.FromWalletID, To: r.ToWalletID,
		Amount: r.Amount, Currency: r.Currency,
		State: State(r.State), FailureReason: r.FailureReason,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		attempts: r.Attempts, version: r.Version, traceContext: r.TraceContext,
	}
}

// NewTransfer is a transfer a user asks for.
type NewTransfer = transferdb.CreateTransferParams

var (
	ErrTransferNotFound = fmt.Errorf("transfer %w", apperr.ErrNotFound)
	// ErrStale means someone else changed the transfer since we read it.
	ErrStale = errors.New("transfer changed since it was read")
)
