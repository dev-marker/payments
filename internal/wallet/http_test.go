package wallet_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/fiston-user/payments/internal/wallet"
	"github.com/google/uuid"
)

// newAPI returns the wallet API on the test database, wired up like main does.
func newAPI(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	wallet.NewHandler(wallet.NewService(newStore(t))).Register(mux)
	return mux
}

// call sends one request to api, and returns the recorded response.
func call(api http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rr
}

// decode reads a JSON response into a T, the way a client would.
func decode[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", rr.Body, err)
	}
	return v
}

// The parts of our responses the tests look at.
type (
	walletJSON struct {
		ID string `json:"id"`
	}
	balanceJSON struct {
		Balance wallet.Money `json:"balance"`
	}
	entriesJSON struct {
		Entries []struct {
			Amount wallet.Money `json:"amount"`
		} `json:"entries"`
		NextCursor string `json:"next_cursor"`
	}
	errorJSON struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
)

func walletBody(owner, currency string) string {
	return fmt.Sprintf(`{"owner_id": %q, "currency": %q}`, owner, currency)
}

// createWallet opens a wallet for a new owner, and returns its ID.
func createWallet(t *testing.T, api http.Handler, currency string) string {
	t.Helper()
	rr := call(api, "POST", "/v1/wallets", walletBody(uuid.NewString(), currency))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create wallet: status %d: %s", rr.Code, rr.Body)
	}
	return decode[walletJSON](t, rr).ID
}

func TestCreateWallet(t *testing.T) {
	t.Parallel()
	api := newAPI(t)
	owner := uuid.NewString()

	// The rows run in order: the duplicate needs the wallet from the first.
	tests := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"created", walletBody(owner, "USD"), 201, ""},
		{"one per currency", walletBody(owner, "USD"), 409, "conflict"},
		{"another currency", walletBody(owner, "JPY"), 201, ""},
		{"lowercase currency", walletBody(owner, "usd"), 422, "invalid_input"},
		{"unsupported currency", walletBody(owner, "XYZ"), 422, "invalid_input"},
		{"owner not a UUID", walletBody("alice", "USD"), 422, "invalid_input"},
		{"unknown field", `{"currency": "USD", "balance": 100}`, 400, "bad_request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := call(api, "POST", "/v1/wallets", tt.body)
			if rr.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tt.status, rr.Body)
			}
			// A success has no error object, so its code is empty.
			if got := decode[errorJSON](t, rr).Error.Code; got != tt.code {
				t.Errorf("code = %q, want %q", got, tt.code)
			}
		})
	}
}

func TestDeposit(t *testing.T) {
	t.Parallel()
	api := newAPI(t)
	id := createWallet(t, api, "USD")

	tests := []struct {
		name   string
		wallet string
		body   string
		status int
	}{
		{"deposited", id, `{"amount": 2500, "currency": "USD"}`, 201},
		{"zero", id, `{"amount": 0, "currency": "USD"}`, 422},
		{"negative", id, `{"amount": -2500, "currency": "USD"}`, 422},
		{"over the maximum", id, `{"amount": 100000001, "currency": "USD"}`, 422},
		{"wrong currency", id, `{"amount": 2500, "currency": "EUR"}`, 422},
		{"decimal amount", id, `{"amount": 25.00, "currency": "USD"}`, 400},
		{"amount as a string", id, `{"amount": "2500", "currency": "USD"}`, 400},
		{"unknown wallet", uuid.NewString(), `{"amount": 2500, "currency": "USD"}`, 404},
		{"malformed wallet ID", "42", `{"amount": 2500, "currency": "USD"}`, 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := call(api, "POST", "/v1/wallets/"+tt.wallet+"/deposits", tt.body)
			if rr.Code != tt.status {
				t.Errorf("status = %d, want %d: %s", rr.Code, tt.status, rr.Body)
			}
		})
	}

	// Only the first deposit was valid, so only it may have moved money.
	rr := call(api, "GET", "/v1/wallets/"+id+"/balance", "")
	if got := decode[balanceJSON](t, rr).Balance; got != (wallet.Money{Amount: 2500, Currency: "USD"}) {
		t.Errorf("balance = %+v, want 2500 USD", got)
	}
}

func TestEntriesPagination(t *testing.T) {
	t.Parallel()
	api := newAPI(t)
	id := createWallet(t, api, "EUR")
	for _, amount := range []int{100, 200, 300} {
		body := fmt.Sprintf(`{"amount": %d, "currency": "EUR"}`, amount)
		if rr := call(api, "POST", "/v1/wallets/"+id+"/deposits", body); rr.Code != http.StatusCreated {
			t.Fatalf("deposit %d: status %d: %s", amount, rr.Code, rr.Body)
		}
	}

	// Follow the cursors, two entries per page, and collect every amount.
	var amounts []int64
	pages, cursor := 0, ""
	for pages < 5 {
		rr := call(api, "GET", "/v1/wallets/"+id+"/entries?limit=2&cursor="+cursor, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rr.Code, rr.Body)
		}
		page := decode[entriesJSON](t, rr)
		for _, e := range page.Entries {
			amounts = append(amounts, e.Amount.Amount)
		}
		pages++
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	// Newest first, and every entry exactly once.
	if want := []int64{300, 200, 100}; !slices.Equal(amounts, want) || pages != 2 {
		t.Errorf("got %v in %d pages, want %v in 2", amounts, pages, want)
	}
}

func TestEntriesRejectsBadQueries(t *testing.T) {
	t.Parallel()
	api := newAPI(t)
	id := createWallet(t, api, "USD")

	for _, query := range []string{"limit=0", "limit=101", "limit=ten", "cursor=not-a-cursor"} {
		t.Run(query, func(t *testing.T) {
			rr := call(api, "GET", "/v1/wallets/"+id+"/entries?"+query, "")
			if rr.Code != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422: %s", rr.Code, rr.Body)
			}
		})
	}
}
