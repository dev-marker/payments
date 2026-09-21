// Package events is the envelope every event we publish travels in.
package events

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// An Event is a fact: something that happened in one of our services. The
// envelope is the same for every event, and Type says what Data holds.
type Event struct {
	ID          uuid.UUID       `json:"id"`           // unique: consumers skip IDs they've seen
	Type        string          `json:"type"`         // like wallet.transfer.completed.v1
	AggregateID uuid.UUID       `json:"aggregate_id"` // what it happened to, like a wallet
	OccurredAt  time.Time       `json:"occurred_at"`
	Data        json.RawMessage `json:"data"`
}

// New returns an event of type typ about aggregate, that happened just now.
func New(typ string, aggregate uuid.UUID, data any) (Event, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Event{}, fmt.Errorf("event %s: %w", typ, err)
	}
	return Event{
		// Version 7 UUIDs start with a timestamp, so they sort by time.
		ID:          uuid.Must(uuid.NewV7()),
		Type:        typ,
		AggregateID: aggregate,
		OccurredAt:  time.Now().UTC(),
		Data:        raw,
	}, nil
}
