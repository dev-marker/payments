// Package transferevents is the transfer service's event contract.
package transferevents

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
)

// Stream holds every event the transfer service publishes.
const Stream = "TRANSFER"

// Event types: a transfer is over, or waits for a person.
const (
	Completed      = "transfer.completed.v1"
	Failed         = "transfer.failed.v1" // refused or reversed: nothing moved
	ReviewRequired = "transfer.review_required.v1"
)

// Subject is where events of type t are published.
func Subject(t string) string {
	return "transfer.events." + strings.TrimPrefix(t, "transfer.")
}

// StreamConfig is the TRANSFER stream, like the WALLET stream.
func StreamConfig(replicas int) jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:       Stream,
		Subjects:   []string{"transfer.events.>"},
		Storage:    jetstream.FileStorage,
		Replicas:   replicas,
		MaxAge:     30 * 24 * time.Hour,
		Duplicates: 10 * time.Minute,
	}
}

// Transfer is the data of every transfer event.
type Transfer struct {
	ID            uuid.UUID `json:"id"`
	UserID        uuid.UUID `json:"user_id"`
	FromWalletID  uuid.UUID `json:"from_wallet_id"`
	ToWalletID    uuid.UUID `json:"to_wallet_id"`
	Amount        int64     `json:"amount"`
	Currency      string    `json:"currency"`
	State         string    `json:"state"`
	FailureReason string    `json:"failure_reason,omitempty"`
}
