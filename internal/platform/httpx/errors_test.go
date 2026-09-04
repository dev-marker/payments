package httpx_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/fiston-user/payments/internal/platform/apperr"
	"github.com/fiston-user/payments/internal/platform/httpx"
)

// errorResponse mirrors the JSON error body, the way a client would see it.
type errorResponse struct {
	Error struct {
		Code      string            `json:"code"`
		Message   string            `json:"message"`
		RequestID string            `json:"request_id"`
		Fields    map[string]string `json:"fields"`
	} `json:"error"`
}

func decodeError(t *testing.T, rr *httptest.ResponseRecorder) errorResponse {
	t.Helper()
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body errorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rr.Body, err)
	}
	return body
}

func TestError(t *testing.T) {
	invalid := &apperr.ValidationError{}
	invalid.Add("amount", "must be positive")
	broke := apperr.NewRule("insufficient_funds", "insufficient funds")

	tests := []struct {
		name    string
		err     error
		status  int
		code    string
		message string
	}{
		{"not found", fmt.Errorf("account 42: %w", apperr.ErrNotFound), 404, "not_found", "account 42: not found"},
		{"conflict", fmt.Errorf("wallet exists: %w", apperr.ErrConflict), 409, "conflict", "wallet exists: conflict"},
		{"validation", invalid.Err(), 422, "invalid_input", "invalid input: amount"},
		{"rule", fmt.Errorf("wallet 42: %w", broke), 422, "insufficient_funds", "wallet 42: insufficient funds"},
		{"internal", errors.New("dial tcp 10.0.0.7:5432: refused"), 500, "internal", "internal server error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			httpx.Error(rr, httptest.NewRequest("GET", "/", nil), tt.err)

			if rr.Code != tt.status {
				t.Errorf("status = %d, want %d", rr.Code, tt.status)
			}
			body := decodeError(t, rr)
			if body.Error.Code != tt.code || body.Error.Message != tt.message {
				t.Errorf("got %q %q, want %q %q", body.Error.Code, body.Error.Message, tt.code, tt.message)
			}
		})
	}
}
