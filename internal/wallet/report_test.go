package wallet_test

import (
	"strings"
	"testing"

	"github.com/fiston-user/payments/internal/wallet"
)

func TestStatementRecord(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		description string
		amount      int64
		currency    string
		want        string // the description, the amount, and the balance: 1,000 minor units more
	}{
		{"dollars", "deposit", 1500, "USD", "deposit | 15.00 | 25.00"},
		{"cents out", "hold", -5, "USD", "hold | -0.05 | 9.95"},
		{"yen", "deposit", 1500, "JPY", "deposit | 1500 | 2500"},
		{"dinars", "deposit", 1500, "KWD", "deposit | 1.500 | 2.500"},
		{"a formula", `=HYPERLINK("https://evil.example","Refund")`, 100, "USD",
			`'=HYPERLINK("https://evil.example","Refund") | 1.00 | 11.00`},
		{"plus", "+1 555 0100", 100, "USD", "'+1 555 0100 | 1.00 | 11.00"},
		{"minus", "-2+3", 100, "USD", "'-2+3 | 1.00 | 11.00"},
		{"at", "@SUM(A1:A9)", 100, "USD", "'@SUM(A1:A9) | 1.00 | 11.00"},
		{"tab", "\t=1+1", 100, "USD", "'\t=1+1 | 1.00 | 11.00"},
		{"inside is fine", "rent = 500", 100, "USD", "rent = 500 | 1.00 | 11.00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := wallet.StatementLine{Description: tt.description, Amount: tt.amount,
				Balance: 1000 + tt.amount, Currency: tt.currency}
			rec, err := line.Record()
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(rec[1:4], " | "); got != tt.want {
				t.Errorf("Record = %q, want %q", got, tt.want)
			}
		})
	}
	if _, err := (wallet.StatementLine{Currency: "XYZ"}).Record(); err == nil {
		t.Error("an unknown currency: err = nil, want an error")
	}
}
