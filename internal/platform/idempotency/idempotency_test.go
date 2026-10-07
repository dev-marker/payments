package idempotency_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/fiston-user/payments/internal/platform/idempotency"
	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// newStore uses the wallet's database: every service's database has the
// idempotency_keys table.
func newStore(t *testing.T) *idempotency.Store {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	pool, err := postgres.NewPool(t.Context(), postgresURL, 4)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return idempotency.NewStore(pool, nil)
}

// newKey returns the key value, sent by caller with a request to move amount.
func newKey(caller, value string, amount int) idempotency.Key {
	// A map of ints always encodes, so there's no error to check.
	fp, _ := idempotency.Fingerprint("POST", "/v1/transfers", map[string]int{"amount": amount})
	return idempotency.Key{Scope: caller, Value: value, Fingerprint: fp}
}

func TestRun(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	alice, bob := uuid.NewString(), uuid.NewString() // two callers, new every run

	// Fake work: runs counts how often work really ran, and each response
	// says which run produced it.
	runs := 0
	created := func(pgx.Tx) (idempotency.Response, error) {
		runs++
		return idempotency.JSON(http.StatusCreated, map[string]int{"run": runs})
	}
	errInvalid := errors.New("invalid input")
	invalid := func(pgx.Tx) (idempotency.Response, error) {
		runs++
		return idempotency.Response{}, errInvalid
	}
	// duplicate sends the same request again while its own work is still
	// running. The duplicate must be refused, and never run.
	duplicate := func(tx pgx.Tx) (idempotency.Response, error) {
		_, err := s.Run(t.Context(), newKey(alice, "c", 500), created)
		if !errors.Is(err, idempotency.ErrInProgress) {
			return idempotency.Response{}, fmt.Errorf("duplicate: got %v, want ErrInProgress", err)
		}
		return created(tx)
	}

	// The rows run in order, and share keys.
	tests := []struct {
		name     string
		caller   string
		key      string
		amount   int
		work     func(pgx.Tx) (idempotency.Response, error)
		err      error
		body     string
		replayed bool
	}{
		{"runs the first time", alice, "a", 500, created, nil, `{"run":1}`, false},
		{"replays a retry", alice, "a", 500, created, nil, `{"run":1}`, true},
		{"refuses a different request", alice, "a", 900, created, idempotency.ErrKeyReused, "", false},
		{"keys are per caller", bob, "a", 500, created, nil, `{"run":2}`, false},
		{"a failure", alice, "b", 500, invalid, errInvalid, "", false},
		{"doesn't burn the key", alice, "b", 500, created, nil, `{"run":4}`, false},
		{"refuses a duplicate in progress", alice, "c", 500, duplicate, nil, `{"run":5}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := s.Run(t.Context(), newKey(tt.caller, tt.key, tt.amount), tt.work)
			if !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}
			body := strings.TrimSpace(string(resp.Body))
			if body != tt.body || resp.Replayed != tt.replayed {
				t.Errorf("got %s replayed=%t, want %s replayed=%t", body, resp.Replayed, tt.body, tt.replayed)
			}
		})
	}
}
