// Package fx is the arithmetic of currency conversion: minor units, exchange
// rates as exact decimals, and rounding that never pays out a fraction of a
// cent we don't have.
package fx

import (
	"fmt"
	"strconv"
	"strings"
)

// RateDecimals is how many decimal places a Rate keeps: more than any provider
// quotes, even for yen to dinars, about 0.0020302.
const RateDecimals = 10

// one is 1, as a Rate: 10 to the power of RateDecimals.
const one = 10_000_000_000

// A Rate is how many units of one currency buy one unit of another, as an
// exact decimal: a whole number of ten-billionths. 0.9215 is 9_215_000_000.
// Never a float64: 0.1 has no exact binary value, and a tiny error, times a
// million dollars, is real money.
type Rate int64

// ParseRate reads a decimal like "0.9215", exactly. strconv.ParseFloat would
// round it to the nearest binary fraction first.
func ParseRate(s string) (Rate, error) {
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" || len(frac) > RateDecimals || strings.Trim(whole+frac, "0123456789") != "" {
		return 0, fmt.Errorf("fx: invalid rate %q", s)
	}
	n, err := strconv.ParseInt(whole+frac+strings.Repeat("0", RateDecimals-len(frac)), 10, 64)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("fx: invalid rate %q", s)
	}
	return Rate(n), nil
}

// String writes r as a decimal, without trailing zeros: 0.9215.
func (r Rate) String() string {
	s := fmt.Sprintf("%d.%0*d", r/one, RateDecimals, r%one)
	return strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
}

// Rates travel as JSON strings, "0.9215": a JSON number is a float64 to most
// of the languages that read it.
func (r Rate) MarshalText() ([]byte, error) {
	return []byte(r.String()), nil
}

func (r *Rate) UnmarshalText(b []byte) (err error) {
	*r, err = ParseRate(string(b))
	return err
}
