// Package notification tells users what happened to their money. It reacts
// to the events other services publish: nobody calls it.
package notification

import (
	"encoding/json"
	"fmt"

	"github.com/fiston-user/payments/internal/platform/events"
	"github.com/fiston-user/payments/internal/transfer/transferevents"
	"github.com/fiston-user/payments/internal/wallet/walletevents"
	"github.com/google/uuid"
)

// A Message is an email to one user, about one event.
type Message struct {
	EventID uuid.UUID
	UserID  uuid.UUID
	To      string // the user's address, from Users
	Subject string
	Body    string
}

// A PermanentError won't go away if we try again, like an event that doesn't
// decode, or a user who doesn't exist. Any other error might.
type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent marks err as one that retries can't fix.
func Permanent(err error) error {
	return &PermanentError{Err: err}
}

// compose writes the message an event deserves. It returns false for events
// we don't tell anyone about.
func compose(evt events.Event) (Message, bool, error) {
	m := Message{EventID: evt.ID}
	switch evt.Type {
	case transferevents.Completed, transferevents.Failed, transferevents.ReviewRequired:
		var t transferevents.Transfer
		if err := json.Unmarshal(evt.Data, &t); err != nil {
			return m, false, Permanent(fmt.Errorf("decode %s: %w", evt.Type, err))
		}
		m.UserID = t.UserID
		if evt.Type == transferevents.Completed {
			m.Subject = "You sent " + money(t.Amount, t.Currency)
			m.Body = "Your transfer went through. Transfer ID: " + t.ID.String()
		} else if evt.Type == transferevents.ReviewRequired {
			m.Subject = "Your transfer of " + money(t.Amount, t.Currency) + " is being reviewed"
			m.Body = "The money is set aside while we check it. We'll email you when it's decided."
		} else {
			m.Subject = "Your transfer of " + money(t.Amount, t.Currency) + " didn't go through"
			m.Body = "Reason: " + t.FailureReason + ". Nothing left your wallet."
		}
	case walletevents.TransferCompleted:
		var t walletevents.Transfer
		if err := json.Unmarshal(evt.Data, &t); err != nil {
			return m, false, Permanent(fmt.Errorf("decode %s: %w", evt.Type, err))
		}
		m.UserID = t.To.OwnerID
		m.Subject = "You received " + money(t.Amount, t.To.Currency)
		m.Body = "It's in your wallet, ready to spend."
	default:
		return m, false, nil
	}
	return m, true, nil
}

// money formats minor units for people: 1500 USD is 15.00 USD. The yen has no
// minor units.
func money(amount int64, currency string) string {
	if currency == "JPY" {
		return fmt.Sprintf("%d JPY", amount)
	}
	return fmt.Sprintf("%d.%02d %s", amount/100, amount%100, currency)
}
