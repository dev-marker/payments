package risk

import "time"

// Limits are the rules' thresholds, with amounts in US cents. They come from
// the configuration: tuning them is the risk team's job, not a release.
type Limits struct {
	MaxCount       int           // transfers per CountWindow: one more is denied
	CountWindow    time.Duration // like ten minutes
	MaxAmount      int64         // sent per AmountWindow: more needs a review
	AmountWindow   time.Duration // like a day: at least CountWindow
	ReviewAbove    int64         // one transfer this big needs a review
	FirstTimeAbove int64         // the most a new wallet, or a new recipient, gets without one
	NewWalletAge   time.Duration // a wallet younger than this is new
}

// Rules returns our rules, in the order they run, with thresholds from l.
func Rules(l Limits) []Rule {
	return []Rule{
		Check{"receiver_frozen", Deny, func(s Signals) bool { return s.ReceiverFrozen }},
		Check{"velocity_count", Deny, func(s Signals) bool { return s.Recent.Count > l.MaxCount }},
		Check{"velocity_amount", Review, func(s Signals) bool { return s.Recent.Amount > l.MaxAmount }},
		Check{"velocity_unknown", Review, func(s Signals) bool { return s.RecentUnknown }},
		Check{"large_amount", Review, func(s Signals) bool { return s.Amount >= l.ReviewAbove }},
		Check{"new_wallet", Review, func(s Signals) bool {
			return s.ReceiverAge < l.NewWalletAge && s.Amount > l.FirstTimeAbove
		}},
		Check{"new_recipient", Review, func(s Signals) bool {
			return !s.PaidBefore && s.Amount > l.FirstTimeAbove
		}},
	}
}
