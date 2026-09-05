// Package idempotency makes requests that move money safe to retry. Clients
// send a unique Idempotency-Key with each request. The first request with a
// key runs. A retry with the same key gets the first response back, byte for
// byte, instead of running again.
package idempotency

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/fiston-user/payments/internal/platform/httpx"
)

// The headers: the key a client sends, and the one that marks a replay.
const (
	KeyHeader      = "Idempotency-Key"
	ReplayedHeader = "Idempotent-Replayed"
)

// Errors for requests that can't run with the key they sent.
var (
	ErrKeyRequired = httpx.NewError(http.StatusBadRequest, "idempotency_key_required",
		"requests that move money need an Idempotency-Key header, of 1 to 255 characters")
	ErrKeyReused = httpx.NewError(http.StatusUnprocessableEntity, "idempotency_key_reused",
		"this Idempotency-Key was already used for a different request")
	ErrInProgress = httpx.NewError(http.StatusConflict, "request_in_progress",
		"a request with this Idempotency-Key is still in progress, retry later")
)

// A Key names one operation: the key a client sent, the caller that sent it,
// and a fingerprint of the request it came with.
type Key struct {
	Scope       string
	Value       string
	Fingerprint []byte
}

// NewKey reads the Idempotency-Key header of r. scope is the caller, and req
// is the decoded request body, for the fingerprint.
func NewKey(r *http.Request, scope string, req any) (Key, error) {
	value := r.Header.Get(KeyHeader)
	if value == "" || len(value) > 255 {
		return Key{}, ErrKeyRequired
	}
	fp, err := Fingerprint(r.Method, r.URL.Path, req)
	if err != nil {
		return Key{}, err
	}
	return Key{Scope: scope, Value: value, Fingerprint: fp}, nil
}

// Fingerprint hashes a request's method, path and canonical body with SHA-256.
// The canonical body is req, the decoded body, encoded again: spaces and the
// order of fields don't change it, but any value does.
func Fingerprint(method, path string, req any) ([]byte, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("fingerprint: %w", err)
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s %s\n", method, path)
	h.Write(body)
	return h.Sum(nil), nil
}

// Response is what we answered for a key.
type Response struct {
	Status   int
	Body     []byte
	Replayed bool // sent before, and stored
}

// JSON returns a response with v as its body, encoded as httpx.WriteJSON would.
func JSON(status int, v any) (Response, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return Response{}, fmt.Errorf("encode response: %w", err)
	}
	return Response{Status: status, Body: append(body, '\n')}, nil
}

// Write sends the response. A replay says so in a header, and is otherwise
// exactly what the first request got.
func (resp Response) Write(w http.ResponseWriter) {
	if resp.Replayed {
		w.Header().Set(ReplayedHeader, "true")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.Status)
	w.Write(resp.Body)
}
