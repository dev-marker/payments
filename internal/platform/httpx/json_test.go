package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fiston-user/payments/internal/platform/httpx"
)

func TestDecodeJSON(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
		want   string
	}{
		{"valid", `{"amount": 500}`, 200, ""},
		{"empty", ``, 400, "body must not be empty"},
		{"malformed", `{"amount": 5`, 400, "malformed JSON"},
		{"wrong type", `{"amount": "500"}`, 400, `field "amount" must be of type int64`},
		{"unknown field", `{"amount": 500, "amout": 5}`, 400, `unknown field "amout"`},
		{"two values", `{"amount": 1}{"amount": 2}`, 400, "body must contain a single JSON value"},
		{"too large", `{"amount": 1, "pad": "` + strings.Repeat("x", 2<<20) + `"}`, 413, "body must be at most 1048576 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					Amount int64 `json:"amount"`
				}
				if err := httpx.DecodeJSON(w, r, &in); err != nil {
					httpx.Error(w, r, err)
					return
				}
				httpx.WriteJSON(w, r, http.StatusOK, in)
			})
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest("POST", "/", strings.NewReader(tt.body)))

			if rr.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tt.status, rr.Body)
			}
			if tt.status != 200 {
				if got := decodeError(t, rr).Error.Message; got != tt.want {
					t.Errorf("message = %q, want %q", got, tt.want)
				}
			}
		})
	}
}
