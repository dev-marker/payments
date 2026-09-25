// Package transfer runs transfers between wallets as sagas.
package transfer

import "slices"

// State is where a transfer is in its saga.
type State string

const (
	Pending      State = "pending"      // accepted: nothing has moved yet
	FundsHeld    State = "funds_held"   // the money left the sender, into a hold
	Completed    State = "completed"    // the receiver has it
	Compensating State = "compensating" // a later step failed: giving the hold back
	Failed       State = "failed"       // refused before anything moved
	Reversed     State = "reversed"     // the hold went back to the sender
)

// transitions is the state machine: every move a transfer can make.
var transitions = map[State][]State{
	Pending:      {FundsHeld, Failed},
	FundsHeld:    {Completed, Compensating},
	Compensating: {Reversed},
}

// CanBecome reports whether a transfer in state s may move to next.
func (s State) CanBecome(next State) bool {
	return slices.Contains(transitions[s], next)
}

// Final reports whether the saga is over.
func (s State) Final() bool {
	return len(transitions[s]) == 0
}
