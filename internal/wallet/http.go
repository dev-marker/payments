package wallet

import (
	"net/http"
	"time"

	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/fiston-user/payments/internal/platform/idempotency"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The API's JSON shapes. They're separate from the database models on
// purpose: those follow the schema, these are a contract with our clients.
type (
	walletResponse struct {
		ID        uuid.UUID  `json:"id"`
		OwnerID   *uuid.UUID `json:"owner_id"`
		Currency  string     `json:"currency"`
		CreatedAt time.Time  `json:"created_at"`
	}
	balanceResponse struct {
		WalletID uuid.UUID `json:"wallet_id"`
		Balance  Money     `json:"balance"`
	}
	depositResponse struct {
		TransactionID uuid.UUID `json:"transaction_id"`
		WalletID      uuid.UUID `json:"wallet_id"`
		Amount        Money     `json:"amount"`
		CreatedAt     time.Time `json:"created_at"`
	}
	transferResponse struct {
		TransactionID uuid.UUID `json:"transaction_id"`
		From          uuid.UUID `json:"from"`
		To            uuid.UUID `json:"to"`
		Amount        Money     `json:"amount"`
		CreatedAt     time.Time `json:"created_at"`
	}
	entryResponse struct {
		ID            int64     `json:"id"`
		TransactionID uuid.UUID `json:"transaction_id"`
		Direction     Direction `json:"direction"`
		Amount        Money     `json:"amount"`
		CreatedAt     time.Time `json:"created_at"`
	}
	entriesResponse struct {
		Entries    []entryResponse `json:"entries"`
		NextCursor string          `json:"next_cursor,omitempty"`
	}
)

func toWallet(a Account) walletResponse {
	return walletResponse{ID: a.ID, OwnerID: a.OwnerID, Currency: a.Currency, CreatedAt: a.CreatedAt}
}

// Handler serves the wallet API over HTTP. It only translates: JSON and URLs
// in, calls to the Service, JSON out. The rules live in the Service.
type Handler struct {
	svc  *Service
	keys *idempotency.Store
}

// NewHandler returns a Handler that serves svc. Requests that move money run
// once per idempotency key, and keys keeps those.
func NewHandler(svc *Service, keys *idempotency.Store) *Handler {
	return &Handler{svc: svc, keys: keys}
}

// Register adds the wallet API's routes to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("POST /v1/wallets", httpx.HandlerFunc(h.createWallet))
	mux.Handle("GET /v1/wallets/{id}", httpx.HandlerFunc(h.getWallet))
	mux.Handle("GET /v1/wallets/{id}/balance", httpx.HandlerFunc(h.getBalance))
	mux.Handle("POST /v1/wallets/{id}/deposits", httpx.HandlerFunc(h.deposit))
	mux.Handle("GET /v1/wallets/{id}/entries", httpx.HandlerFunc(h.listEntries))
	mux.Handle("POST /v1/transfers", httpx.HandlerFunc(h.transfer))
}

// walletID reads the wallet ID from the URL. An ID that isn't a UUID can't
// name a wallet, so it's a 404, just like an unknown one.
func walletID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, ErrWalletNotFound
	}
	return id, nil
}

// caller names who sent a request. Clients choose their idempotency keys, so a
// key is only unique per caller. Until we authenticate callers, every request
// comes from the same one.
func caller(*http.Request) string {
	return "anonymous"
}

// idempotent runs a request that moves money once per idempotency key. req is
// the decoded request body, for the key's fingerprint. do runs inside the
// database transaction that completes the key, with a Service bound to it, so
// the money moves and the key completes together, or not at all.
func (h *Handler) idempotent(
	w http.ResponseWriter, r *http.Request, req any,
	do func(svc *Service) (idempotency.Response, error),
) error {
	key, err := idempotency.NewKey(r, caller(r), req)
	if err != nil {
		return err
	}
	resp, err := h.keys.Run(r.Context(), key, func(tx pgx.Tx) (idempotency.Response, error) {
		return do(h.svc.WithTx(tx))
	})
	if err != nil {
		return err
	}
	resp.Write(w)
	return nil
}

func (h *Handler) createWallet(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		OwnerID  string `json:"owner_id"`
		Currency string `json:"currency"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	acc, err := h.svc.CreateWallet(r.Context(), req.OwnerID, req.Currency)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/wallets/"+acc.ID.String())
	httpx.WriteJSON(w, r, http.StatusCreated, toWallet(acc))
	return nil
}

func (h *Handler) getWallet(w http.ResponseWriter, r *http.Request) error {
	id, err := walletID(r)
	if err != nil {
		return err
	}
	acc, err := h.svc.Wallet(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, r, http.StatusOK, toWallet(acc))
	return nil
}

func (h *Handler) getBalance(w http.ResponseWriter, r *http.Request) error {
	id, err := walletID(r)
	if err != nil {
		return err
	}
	b, err := h.svc.Balance(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, r, http.StatusOK, balanceResponse{
		WalletID: b.AccountID,
		Balance:  Money{Amount: b.Balance, Currency: b.Currency},
	})
	return nil
}

func (h *Handler) deposit(w http.ResponseWriter, r *http.Request) error {
	id, err := walletID(r)
	if err != nil {
		return err
	}
	var amount Money
	if err := httpx.DecodeJSON(w, r, &amount); err != nil {
		return err
	}
	return h.idempotent(w, r, amount, func(svc *Service) (idempotency.Response, error) {
		tx, err := svc.Deposit(r.Context(), id, amount)
		if err != nil {
			return idempotency.Response{}, err
		}
		return idempotency.JSON(http.StatusCreated, depositResponse{
			TransactionID: tx.ID,
			WalletID:      id,
			Amount:        amount,
			CreatedAt:     tx.CreatedAt,
		})
	})
}

func (h *Handler) transfer(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		From     string `json:"from"`
		To       string `json:"to"`
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	return h.idempotent(w, r, req, func(svc *Service) (idempotency.Response, error) {
		amount := Money{Amount: req.Amount, Currency: req.Currency}
		tr, err := svc.Transfer(r.Context(), req.From, req.To, amount)
		if err != nil {
			return idempotency.Response{}, err
		}
		return idempotency.JSON(http.StatusCreated, transferResponse{
			TransactionID: tr.TransactionID,
			From:          tr.From,
			To:            tr.To,
			Amount:        tr.Amount,
			CreatedAt:     tr.CreatedAt,
		})
	})
}

func (h *Handler) listEntries(w http.ResponseWriter, r *http.Request) error {
	id, err := walletID(r)
	if err != nil {
		return err
	}
	limit, err := httpx.QueryInt(r, "limit", DefaultPageSize)
	if err != nil {
		return err
	}
	page, err := h.svc.Entries(r.Context(), id, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		return err
	}
	resp := entriesResponse{
		Entries:    make([]entryResponse, len(page.Entries)),
		NextCursor: page.NextCursor,
	}
	for i, e := range page.Entries {
		resp.Entries[i] = entryResponse{
			ID:            e.ID,
			TransactionID: e.TransactionID,
			Direction:     e.Direction,
			Amount:        Money{Amount: e.Amount, Currency: e.Currency},
			CreatedAt:     e.CreatedAt,
		}
	}
	httpx.WriteJSON(w, r, http.StatusOK, resp)
	return nil
}
