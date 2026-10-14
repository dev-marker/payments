package signature_test

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/fiston-user/payments/internal/webhook/signature"
)

func TestVerify(t *testing.T) {
	t.Parallel()
	secret := signature.NewSecret()
	now := time.Now()
	body := []byte(`{"type":"transfer.completed.v1","data":{"amount":1500}}`)
	signed := func(secret string, at time.Time) http.Header {
		h := http.Header{}
		if err := signature.Sign(h, secret, "evt_1", at, body); err != nil {
			t.Fatalf("Sign: %v", err)
		}
		return h
	}
	// An attacker re-sends an old message, with a fresh timestamp.
	retimed := signed(secret, now.Add(-time.Hour))
	retimed.Set(signature.HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))

	tests := []struct {
		name   string
		header http.Header
		body   []byte
		want   error
	}{
		{"valid", signed(secret, now), body, nil},
		{"tampered body", signed(secret, now), []byte(`{"type":"transfer.completed.v1","data":{"amount":9999}}`),
			signature.ErrNoMatch},
		{"wrong secret", signed(signature.NewSecret(), now), body, signature.ErrNoMatch},
		{"ten minutes old", signed(secret, now.Add(-10*time.Minute)), body, signature.ErrTimestamp},
		{"old, with a new timestamp", retimed, body, signature.ErrNoMatch},
		{"no headers", http.Header{}, body, signature.ErrMissingHeaders},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := signature.Verify(tt.header, secret, tt.body, now); !errors.Is(err, tt.want) {
				t.Errorf("Verify = %v, want %v", err, tt.want)
			}
		})
	}
}
