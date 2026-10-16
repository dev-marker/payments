package fx_test

import (
	"testing"

	"github.com/fiston-user/payments/internal/fx"
)

// rate parses a rate, or fails the test.
func rate(t *testing.T, s string) fx.Rate {
	t.Helper()
	r, err := fx.ParseRate(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestParseRate(t *testing.T) {
	for _, s := range []string{"0.9215", "151.37", "0.0066063", "1", "0.0000000001"} {
		if r := rate(t, s); r.String() != s {
			t.Errorf("ParseRate(%q).String() = %q", s, r)
		}
	}
	for _, s := range []string{"", "0", "-1", "+1", "1e3", ".5", "1.2.3", "0.00000000001", "NaN"} {
		if r, err := fx.ParseRate(s); err == nil {
			t.Errorf("ParseRate(%q) = %v, want an error", s, r)
		}
	}
}

func TestConvert(t *testing.T) {
	tests := []struct {
		name     string
		amount   int64
		from, to string
		rate     string
		rounding fx.Rounding
		want     int64
	}{
		{"dollars to euros", 10000, "USD", "EUR", "0.9215", fx.Down, 9215},
		{"half a cent, down", 1000, "USD", "EUR", "0.9215", fx.Down, 921}, // 921.5
		{"half a cent, up", 1000, "USD", "EUR", "0.9215", fx.Up, 922},
		{"dollars to yen", 1000, "USD", "JPY", "151.37", fx.Down, 1513}, // 1,513.7 yen
		{"yen to dollars", 1000, "JPY", "USD", "0.0066063", fx.Down, 660},
		{"a yen is less than a cent", 1, "JPY", "USD", "0.0066063", fx.Down, 0},
		{"dollars to dinars", 10000, "USD", "KWD", "0.3071", fx.Down, 30710}, // 30.710 KWD
		{"a million dollars in yen", 100_000_000, "USD", "JPY", "151.37", fx.Down, 151_370_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := fx.Convert(tt.amount, tt.from, tt.to, rate(t, tt.rate), tt.rounding)
			if err != nil || got != tt.want {
				t.Errorf("Convert = %d, %v; want %d", got, err, tt.want)
			}
		})
	}
}

func TestCost(t *testing.T) {
	tests := []struct {
		name     string
		amount   int64
		from, to string
		rate     string
		want     int64
	}{
		{"fifty euros", 5000, "USD", "EUR", "0.9215", 5426}, // 54.259
		{"a thousand yen", 1000, "USD", "JPY", "151.37", 661},
		{"exactly", 9215, "USD", "EUR", "0.9215", 10000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := rate(t, tt.rate)
			got, err := fx.Cost(tt.amount, tt.from, tt.to, r)
			if err != nil || got != tt.want {
				t.Fatalf("Cost = %d, %v; want %d", got, err, tt.want)
			}
			// The cheapest that buys enough: a cent less doesn't.
			enough, _ := fx.Convert(got, tt.from, tt.to, r, fx.Down)
			short, _ := fx.Convert(got-1, tt.from, tt.to, r, fx.Down)
			if enough < tt.amount || short >= tt.amount {
				t.Errorf("%d buys %d, and %d buys %d: want %d", got, enough, got-1, short, tt.amount)
			}
		})
	}
}

func TestFee(t *testing.T) {
	for _, tt := range []struct{ amount, bps, want int64 }{
		{10000, 50, 50}, // half a percent of 100.00
		{5426, 50, 28},  // 27.13, up
		{1, 50, 1},      // never free
		{10000, 0, 0},
	} {
		if got := fx.Fee(tt.amount, tt.bps); got != tt.want {
			t.Errorf("Fee(%d, %d) = %d, want %d", tt.amount, tt.bps, got, tt.want)
		}
	}
}
