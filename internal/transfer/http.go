package transfer

import (
	"net/http"
	"regexp"

	"github.com/fiston-user/payments/internal/platform/apperr"
	"github.com/fiston-user/payments/internal/platform/authn"
	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/fiston-user/payments/internal/platform/idempotency"
	"github.com/google/uuid"
)

// Handler serves the transfer API. It's asynchronous: a new transfer gets a
// 202 Accepted at once, and a URL to follow it.
type Handler struct {
	store *Store
	wake  func()
}

// NewHandler returns a Handler for store.
func NewHandler(store *Store, wake func()) *Handler {
	return &Handler{store: store, wake: wake}
}

// Register adds the transfer routes to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("POST /v1/transfers", httpx.HandlerFunc(h.create))
	mux.Handle("GET /v1/transfers/{id}", httpx.HandlerFunc(h.get))
}

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		From     string `json:"from"`
		To       string `json:"to"`
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	key := r.Header.Get(idempotency.KeyHeader)
	if key == "" || len(key) > 255 {
		return idempotency.ErrKeyRequired
	}
	// Only what we can check alone: the rest is the wallet's call.
	var v apperr.ValidationError
	from, err := uuid.Parse(req.From)
	if err != nil {
		v.Add("from", "must be a wallet ID")
	}
	to, err := uuid.Parse(req.To)
	if err != nil {
		v.Add("to", "must be a wallet ID")
	} else if to == from {
		v.Add("to", "must be a different wallet")
	}
	if req.Amount <= 0 {
		v.Add("amount", "must be a positive number of minor units, like 1050 for 10.50")
	}
	if !currencyCode.MatchString(req.Currency) {
		v.Add("currency", "must be a three-letter ISO 4217 code, like USD")
	}
	if err := v.Err(); err != nil {
		return err
	}
	user, _ := authn.UserID(r.Context())
	nt := NewTransfer{UserID: user, IdempotencyKey: key, FromWalletID: from, ToWalletID: to,
		Amount: req.Amount, Currency: req.Currency}
	t, created, err := h.store.Create(r.Context(), nt)
	if err != nil {
		return err
	}
	if !created {
		// A retry: the same request gets the same transfer.
		if t.From != from || t.To != to || t.Amount != req.Amount || t.Currency != req.Currency {
			return idempotency.ErrKeyReused
		}
		w.Header().Set(idempotency.ReplayedHeader, "true")
	}
	w.Header().Set("Location", "/v1/transfers/"+t.ID.String())
	httpx.WriteJSON(w, r, http.StatusAccepted, t)
	if created {
		h.wake()
	}
	return nil
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return ErrTransferNotFound
	}
	t, err := h.store.Get(r.Context(), id)
	if err != nil {
		return err
	}
	// Other people's transfers don't exist.
	if user, _ := authn.UserID(r.Context()); t.UserID != user {
		return ErrTransferNotFound
	}
	httpx.WriteJSON(w, r, http.StatusOK, t)
	return nil
}
