package fx

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrNoRate means a provider has no rate for a pair of currencies.
var ErrNoRate = errors.New("fx: no rate")

// A Provider knows the current mid-market rates: a bank, or a market data
// feed, in production. Rate says how many units of to one unit of from buys.
// It returns an error rather than a stale rate: no quote beats a wrong one.
type Provider interface {
	Rate(ctx context.Context, from, to string) (Rate, error)
}

// Static is a Provider with fixed rates, by pair, like "USD/EUR". For
// development and tests: no network, and the same numbers every time.
type Static map[string]Rate

func (s Static) Rate(_ context.Context, from, to string) (Rate, error) {
	r, ok := s[from+"/"+to]
	if !ok {
		return 0, fmt.Errorf("%w for %s/%s", ErrNoRate, from, to)
	}
	return r, nil
}

//go:embed devrates.json
var devRates []byte

// DevRates are the development rates, from devrates.json, built into the binary.
func DevRates() (Static, error) {
	var s Static
	if err := json.Unmarshal(devRates, &s); err != nil {
		return nil, fmt.Errorf("devrates.json: %w", err)
	}
	return s, nil
}
