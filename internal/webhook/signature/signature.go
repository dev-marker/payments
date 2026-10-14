// Package signature signs webhooks, and verifies them on the receiving side.
// The scheme is Standard Webhooks': an HMAC-SHA256 of the message's ID, its
// timestamp and its body, sent in three headers.
package signature

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The headers every delivery carries.
const (
	HeaderID        = "Webhook-Id"
	HeaderTimestamp = "Webhook-Timestamp"
	HeaderSignature = "Webhook-Signature"
)

// Tolerance is how far a message's timestamp may be from the receiver's clock.
const Tolerance = 5 * time.Minute

// Why Verify refused a message.
var (
	ErrMissingHeaders = errors.New("signature: missing webhook headers")
	ErrTimestamp      = errors.New("signature: timestamp outside the tolerance")
	ErrNoMatch        = errors.New("signature: no signature matches")
)

const secretPrefix = "whsec_"

// NewSecret returns a new random secret: whsec_, then 32 bytes in base64.
func NewSecret() string {
	key := make([]byte, 32)
	rand.Read(key) // never fails, says crypto/rand
	return secretPrefix + base64.StdEncoding.EncodeToString(key)
}

// mac returns v1, then the base64 HMAC-SHA256 of id.timestamp.body.
func mac(secret, id string, ts int64, body []byte) (string, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, secretPrefix))
	if err != nil {
		return "", fmt.Errorf("signature: invalid secret: %w", err)
	}
	h := hmac.New(sha256.New, key)
	fmt.Fprintf(h, "%s.%d.", id, ts)
	h.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}

// Sign sets the three headers on h, for a message with this id and body,
// sent at time at.
func Sign(h http.Header, secret, id string, at time.Time, body []byte) error {
	sig, err := mac(secret, id, at.Unix(), body)
	if err != nil {
		return err
	}
	h.Set(HeaderID, id)
	h.Set(HeaderTimestamp, strconv.FormatInt(at.Unix(), 10))
	h.Set(HeaderSignature, sig)
	return nil
}

// Verify checks that a message was signed with secret, and recently: now is
// the receiver's clock. Run it on the raw body, before decoding anything.
func Verify(h http.Header, secret string, body []byte, now time.Time) error {
	id, ts, sigs := h.Get(HeaderID), h.Get(HeaderTimestamp), h.Get(HeaderSignature)
	if id == "" || ts == "" || sigs == "" {
		return ErrMissingHeaders
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ErrTimestamp
	}
	if d := now.Sub(time.Unix(sec, 0)); d > Tolerance || d < -Tolerance {
		return ErrTimestamp
	}
	want, err := mac(secret, id, sec, body)
	if err != nil {
		return err
	}
	// Several signatures, separated by spaces, while a secret is rotated.
	for sig := range strings.FieldsSeq(sigs) {
		// In constant time: == stops at the first wrong byte, and how long it
		// takes tells an attacker how many bytes they got right.
		if hmac.Equal([]byte(sig), []byte(want)) {
			return nil
		}
	}
	return ErrNoMatch
}
