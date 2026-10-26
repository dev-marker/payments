package fx

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// exponents is how many digits each currency has after the decimal point, from
// ISO 4217: a dollar is 100 cents, a yen has no minor unit, and a Kuwaiti dinar
// is 1,000 fils.
var exponents = map[string]int{
	"EUR": 2,
	"GBP": 2,
	"JPY": 0,
	"KWD": 3,
	"USD": 2,
}

// Rounding says which way an amount goes when it falls between two minor units.
type Rounding int

const (
	// Down is for what we pay out: never a fraction of a cent more than the rate gives.
	Down Rounding = iota
	// Up is for what we charge: never a fraction of a cent less than the rate asks.
	Up
)

// ErrTooLarge is a conversion whose result doesn't fit in an int64.
var ErrTooLarge = errors.New("fx: amount too large")

// Convert returns amount, in minor units of from, in minor units of to, at
// rate, rounded r. The rate is per major unit, so the exponents move the
// decimal point: 1,000 yen at 0.0066063 is 6.6063 dollars, 660 cents, rounded down.
func Convert(amount int64, from, to string, rate Rate, r Rounding) (int64, error) {
	num, den, err := factor(from, to, rate)
	if err != nil {
		return 0, err
	}
	return mulDiv(amount, num, den, r)
}

// Cost is Convert backwards: the minor units of from that buy amount minor
// units of to, at rate. It rounds up, to the smallest amount that Convert,
// rounding down, turns into amount or more.
func Cost(amount int64, from, to string, rate Rate) (int64, error) {
	num, den, err := factor(from, to, rate)
	if err != nil {
		return 0, err
	}
	return mulDiv(amount, den, num, Up)
}

// factor returns the fraction that turns minor units of from into minor units
// of to: rate × 10^(to's exponent) ÷ 10^(RateDecimals + from's exponent).
func factor(from, to string, rate Rate) (num, den *big.Int, err error) {
	ef, ok := exponents[from]
	et, ok2 := exponents[to]
	if !ok || !ok2 || rate <= 0 {
		return nil, nil, fmt.Errorf("fx: can't convert %s to %s at %s", from, to, rate)
	}
	num = new(big.Int).Mul(big.NewInt(int64(rate)), pow10(et))
	return num, pow10(RateDecimals + ef), nil
}

// mulDiv returns amount × num ÷ den, rounded r. It works in math/big: a
// million dollars in cents, times a rate in ten-billionths, is already
// bigger than the largest int64, and an int64 overflows without a word.
func mulDiv(amount int64, num, den *big.Int, r Rounding) (int64, error) {
	n := new(big.Int).Mul(big.NewInt(amount), num)
	q, rem := n.QuoRem(n, den, new(big.Int))
	if r == Up && rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, ErrTooLarge
	}
	return q.Int64(), nil
}

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

// Fee returns bps basis points of amount, a hundredth of a percent each,
// rounded up: 50 basis points of 54.26 dollars is 28 cents, not 27.13.
func Fee(amount, bps int64) int64 {
	return (amount*bps + 9_999) / 10_000
}

// Format writes amount, in minor units of currency, as a decimal with the
// currency's own number of places: 1500 USD is 15.00, -5 USD is -0.05, 1500 JPY
// is 1500, and 1500 KWD is 1.500. It works on the digits: floats never touch money.
func Format(amount int64, currency string) (string, error) {
	exp, ok := exponents[currency]
	if !ok {
		return "", fmt.Errorf("fx: unknown currency %q", currency)
	}
	digits, sign := strconv.FormatInt(amount, 10), ""
	if amount < 0 {
		digits, sign = digits[1:], "-"
	}
	if exp == 0 {
		return sign + digits, nil
	}
	if len(digits) <= exp {
		digits = strings.Repeat("0", exp-len(digits)+1) + digits // 5 cents is 005
	}
	cut := len(digits) - exp
	return sign + digits[:cut] + "." + digits[cut:], nil
}
