package transfer_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/fiston-user/payments/internal/transfer"
	"github.com/google/uuid"
)

// newAPI serves the transfer API on the package's database. Nothing runs
// the sagas: we only look at what the API answers.
func newAPI(t *testing.T) http.Handler {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	pool, err := postgres.NewPool(t.Context(), postgresURL, 4)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	mux := http.NewServeMux()
	transfer.NewHandler(transfer.NewStore(pool), func() {}).Register(mux)
	return mux
}

// body is a transfer request.
func body(from, to string, amount int64, currency string) string {
	return fmt.Sprintf(`{"from": %q, "to": %q, "amount": %d, "currency": %q}`, from, to, amount, currency)
}

func TestCreateValidation(t *testing.T) {
	t.Parallel()
	api := newAPI(t)
	from, to := uuid.NewString(), uuid.NewString()

	// One valid request, then every way to get one wrong, one at a time.
	tests := []struct {
		name  string
		body  string
		field string // the one field that's wrong: none for a valid request
	}{
		{"valid", body(from, to, 1500, "USD"), ""},
		{"from is not an ID", body("alice", to, 1500, "USD"), "from"},
		{"to is not an ID", body(from, "bob", 1500, "USD"), "to"},
		{"to is from", body(from, from, 1500, "USD"), "to"},
		{"zero amount", body(from, to, 0, "USD"), "amount"},
		{"negative amount", body(from, to, -1500, "USD"), "amount"},
		{"over a million dollars", body(from, to, 100_000_001, "USD"), "amount"},
		{"lowercase currency", body(from, to, 1500, "usd"), "currency"},
		{"no currency", body(from, to, 1500, ""), "currency"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest("POST", "/v1/transfers", strings.NewReader(tt.body))
			req.Header.Set("Idempotency-Key", uuid.NewString())
			rr := httptest.NewRecorder()
			api.ServeHTTP(rr, req)

			want := http.StatusAccepted
			if tt.field != "" {
				want = http.StatusUnprocessableEntity
			}
			if rr.Code != want {
				t.Fatalf("status = %d, want %d: %s", rr.Code, want, rr.Body)
			}
			var resp struct {
				Error struct {
					Fields map[string]string `json:"fields"`
				} `json:"error"`
			}
			json.Unmarshal(rr.Body.Bytes(), &resp)
			if f := resp.Error.Fields; tt.field != "" && (len(f) != 1 || f[tt.field] == "") {
				t.Errorf("errors %v, want one, about %s", f, tt.field)
			}
		})
	}
}
