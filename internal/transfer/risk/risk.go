// Package risk decides whether a transfer may go ahead: allow it, ask a
// person to review it, or deny it. Every decision comes with its reasons.
package risk

import "time"

// A Decision is what we do with a transfer. They're ordered: when rules
// disagree, the strongest one wins.
type Decision int

const (
	Allow  Decision = iota // go ahead
	Review                 // hold the money, and ask a person
	Deny                   // refuse, before any money moves
)

func (d Decision) String() string {
	return [...]string{"allow", "review", "deny"}[d]
}

// Signals are what we know about a transfer when we decide. They're gathered
// first, so rules never do I/O: they're fast, and trivial to test.
type Signals struct {
	Amount         int64         // in US cents, whatever the currency
	Recent         Recent        // the sender's recent transfers, this one included
	RecentUnknown  bool          // Redis didn't answer
	ReceiverFrozen bool          // the receiving wallet can't be paid
	ReceiverAge    time.Duration // since the receiving wallet was opened
	PaidBefore     bool          // the sender has paid this wallet before
}

// A Rule looks at a transfer's signals, and asks for a decision. Anything
// other than Allow comes with a reason.
type Rule interface {
	Evaluate(s Signals) (Decision, string)
}

// Check is the usual kind of Rule: a condition, and what it asks for.
type Check struct {
	Name     string // the reason it gives: stable, so we can count it
	Decision Decision
	Match    func(s Signals) bool
}

func (c Check) Evaluate(s Signals) (Decision, string) {
	if c.Match(s) {
		return c.Decision, c.Name
	}
	return Allow, ""
}

// An Assessment is the engine's answer: the strongest decision any rule asked
// for, and every reason, in the order the rules ran.
type Assessment struct {
	Decision Decision
	Reasons  []string
}

// An Engine runs rules, in order.
type Engine struct {
	rules []Rule
}

// NewEngine returns an Engine that runs rules, in that order.
func NewEngine(rules ...Rule) *Engine {
	return &Engine{rules: rules}
}

// Evaluate runs every rule, even after a Deny: a record of everything that
// was wrong is worth more than the microseconds we'd save.
func (e *Engine) Evaluate(s Signals) Assessment {
	var a Assessment
	for _, r := range e.rules {
		if d, reason := r.Evaluate(s); d != Allow {
			a.Decision = max(a.Decision, d)
			a.Reasons = append(a.Reasons, reason)
		}
	}
	return a
}
