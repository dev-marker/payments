// Package walletevents is the wallet's event contract: the stream other
// services read, the events in it, and what each event's data holds.
package walletevents

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
)

// Stream is the JetStream stream with every event the wallet publishes.
const Stream = "WALLET"

// Event types. A type names the service, what happened, and the version of
// its data. A breaking change is a new version, published next to the old one.
const (
	WalletCreated     = "wallet.wallet.created.v1"
	DepositCompleted  = "wallet.deposit.completed.v1"
	TransferCompleted = "wallet.transfer.completed.v1"
)

// Subject is the subject events of type t are published on, like
// wallet.events.transfer.completed.v1. Consumers can filter on parts of it.
func Subject(t string) string {
	return "wallet.events." + strings.TrimPrefix(t, "wallet.")
}

// StreamConfig is the WALLET stream, kept on replicas servers.
func StreamConfig(replicas int) jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:        Stream,
		Description: "Everything that happens to wallets",
		Subjects:    []string{"wallet.events.>"},
		Storage:     jetstream.FileStorage,
		Replicas:    replicas,
		// Events stay for 30 days, read or not, so a new consumer can replay
		// history, and a broken one can catch up after a fix.
		Retention: jetstream.LimitsPolicy,
		MaxAge:    30 * 24 * time.Hour,
		MaxBytes:  1 << 30,
		Discard:   jetstream.DiscardOld,
		// A message with a Nats-Msg-Id seen in the last ten minutes is
		// acknowledged, and dropped: publishing twice stores it once.
		Duplicates: 10 * time.Minute,
	}
}

// Wallet is a wallet, as events describe it.
type Wallet struct {
	ID       uuid.UUID `json:"id"`
	OwnerID  uuid.UUID `json:"owner_id"`
	Currency string    `json:"currency"`
}

// Deposit is the data of a deposit.completed event: money came into a wallet
// from outside.
type Deposit struct {
	TransactionID uuid.UUID `json:"transaction_id"`
	Wallet        Wallet    `json:"wallet"`
	Amount        int64     `json:"amount"` // minor units of the wallet's currency
}

// Transfer is the data of a transfer.completed event: money moved from one
// wallet to another.
type Transfer struct {
	TransactionID uuid.UUID `json:"transaction_id"`
	From          Wallet    `json:"from"`
	To            Wallet    `json:"to"`
	Amount        int64     `json:"amount"`
}
