package wallet_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/fiston-user/payments/internal/platform/idempotency"
	"github.com/fiston-user/payments/internal/wallet"
	"github.com/google/uuid"
)

// newAPI returns the wallet API on the test database, wired up like main does,
// without the Redis cache: Postgres alone must get idempotency right.
func newAPI(t *testing.T) http.Handler {
	t.Helper()
	pool := newPool(t)
	svc := wallet.NewService(wallet.NewStore(pool))
	mux := http.NewServeMux()
	wallet.NewHandler(svc, idempotency.NewStore(pool, nil)).Register(mux)
	return mux
}

// call sends one request to api, and returns the recorded response. Each call
// has a fresh idempotency key, like a new request from a well-behaved client.
func call(api http.Handler, method, path, body string) *httptest.ResponseRecorder {
	return callWithKey(api, method, path, uuid.NewString(), body)
}

// callWithKey sends a request with the idempotency key we pick: the same key
// again is a retry.
func callWithKey(api http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(idempotency.KeyHeader, key)
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, req)
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

func TestTransferEndpoint(t *testing.T) {
	t.Parallel()
	api := newAPI(t)
	alice, bob, euros := createWallet(t, api, "USD"), createWallet(t, api, "USD"), createWallet(t, api, "EUR")
	if rr := call(api, "POST", "/v1/wallets/"+alice+"/deposits", `{"amount": 1000, "currency": "USD"}`); rr.Code != http.StatusCreated {
		t.Fatalf("deposit: status %d: %s", rr.Code, rr.Body)
	}

	// The rows run in order: the second transfer needs the money the first one spent.
	tests := []struct {
		name     string
		from, to string
		amount   int64
		currency string
		status   int
		code     string
	}{
		{"transferred", alice, bob, 600, "USD", 201, ""},
		{"insufficient funds", alice, bob, 600, "USD", 422, "insufficient_funds"},
		{"to the same wallet", alice, alice, 100, "USD", 422, "invalid_input"},
		{"to another currency", alice, euros, 100, "USD", 422, "invalid_input"},
		{"wrong currency", alice, bob, 100, "EUR", 422, "invalid_input"},
		{"unknown wallet", alice, uuid.NewString(), 100, "USD", 422, "invalid_input"},
		{"zero", alice, bob, 0, "USD", 422, "invalid_input"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"from": %q, "to": %q, "amount": %d, "currency": %q}`, tt.from, tt.to, tt.amount, tt.currency)
			rr := call(api, "POST", "/v1/transfers", body)
			if rr.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tt.status, rr.Body)
			}
			if got := decode[errorJSON](t, rr).Error.Code; got != tt.code {
				t.Errorf("code = %q, want %q", got, tt.code)
			}
		})
	}

	// Six dollars moved, exactly once.
	for id, want := range map[string]int64{alice: 400, bob: 600} {
		rr := call(api, "GET", "/v1/wallets/"+id+"/balance", "")
		if got := decode[balanceJSON](t, rr).Balance.Amount; got != want {
			t.Errorf("balance of %s = %d, want %d", id, got, want)
		}
	}
}

func TestTransferRetryStorm(t *testing.T) {
	t.Parallel()
	api := newAPI(t)
	alice, bob := createWallet(t, api, "USD"), createWallet(t, api, "USD")
	deposit := call(api, "POST", "/v1/wallets/"+alice+"/deposits", `{"amount": 1000, "currency": "USD"}`)
	if deposit.Code != http.StatusCreated {
		t.Fatalf("deposit: status %d: %s", deposit.Code, deposit.Body)
	}

	// One transfer, sent twenty times at once with the same key: a client that
	// retries in a panic. Collect every response.
	key := uuid.NewString()
	body := fmt.Sprintf(`{"from": %q, "to": %q, "amount": 100, "currency": "USD"}`, alice, bob)
	responses := make(chan *httptest.ResponseRecorder, 20)
	concurrently(20, func() error {
		responses <- callWithKey(api, "POST", "/v1/transfers", key, body)
		return nil
	})
	close(responses)

	// Exactly one ran. The rest were refused while it was running, or got its
	// response replayed, byte for byte.
	ran, replayed, refused := 0, 0, 0
	bodies := make(map[string]bool)
	for rr := range responses {
		switch {
		case rr.Code == http.StatusConflict:
			refused++
			continue
		case rr.Code != http.StatusCreated:
			t.Fatalf("status = %d: %s", rr.Code, rr.Body)
		case rr.Header().Get(idempotency.ReplayedHeader) == "true":
			replayed++
		default:
			ran++
		}
		bodies[rr.Body.String()] = true
	}
	t.Logf("%d ran, %d replayed, %d refused as in progress", ran, replayed, refused)
	if ran != 1 || len(bodies) != 1 {
		t.Errorf("%d requests ran, with %d different bodies; want exactly 1 of each", ran, len(bodies))
	}

	// Now that it's done, a retry gets the stored response.
	rr := callWithKey(api, "POST", "/v1/transfers", key, body)
	if rr.Header().Get(idempotency.ReplayedHeader) != "true" || !bodies[rr.Body.String()] {
		t.Errorf("retry: status %d, not the stored response: %s", rr.Code, rr.Body)
	}

	// And the money moved once: one dollar, not twenty.
	for id, want := range map[string]int64{alice: 900, bob: 100} {
		rr := call(api, "GET", "/v1/wallets/"+id+"/balance", "")
		if got := decode[balanceJSON](t, rr).Balance.Amount; got != want {
			t.Errorf("balance of %s = %d, want %d", id, got, want)
		}
	}
}
