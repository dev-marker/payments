package wallet

import (
	"context"
	"encoding/csv"
	"log/slog"
	"net/http"
	"time"

	"github.com/fiston-user/payments/internal/fx"
	"github.com/fiston-user/payments/internal/platform/apperr"
	"github.com/fiston-user/payments/internal/platform/authn"
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
	// A quote, and once it's used, a conversion: the same, with its transaction.
	quoteResponse struct {
		ID            uuid.UUID  `json:"id"`
		Sell          Money      `json:"sell"`
		Buy           Money      `json:"buy"`
		Fee           Money      `json:"fee"`
		Rate          fx.Rate    `json:"rate"`
		ExpiresAt     time.Time  `json:"expires_at"`
		TransactionID *uuid.UUID `json:"transaction_id,omitempty"`
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
	mux.Handle("GET /v1/wallets/{id}/statement.csv", httpx.HandlerFunc(h.statement))
	mux.Handle("POST /v1/transfers", httpx.HandlerFunc(h.transfer))
	mux.Handle("POST /v1/fx/quotes", httpx.HandlerFunc(h.quote))
	mux.Handle("POST /v1/fx/conversions", httpx.HandlerFunc(h.convert))
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

// caller returns the user who sent a request, as authn.Require stored it. If a
// route ever misses Require, it's uuid.Nil, who owns nothing: it fails closed.
func caller(ctx context.Context) uuid.UUID {
	user, _ := authn.UserID(ctx)
	return user
}

// as returns the Service acting for the user who sent r.
func (h *Handler) as(r *http.Request) *Service {
	return h.svc.As(caller(r.Context()))
}

// idempotent runs a request that moves money once per idempotency key. req is
// the decoded request body, for the key's fingerprint. do runs inside the
// database transaction that completes the key, with a Service bound to it, so
// the money moves and the key completes together, or not at all.
func (h *Handler) idempotent(
	w http.ResponseWriter, r *http.Request, req any,
	do func(svc *Service) (idempotency.Response, error),
) error {
	key, err := idempotency.NewKey(r, caller(r.Context()).String(), req)
	if err != nil {
		return err
	}
	resp, err := h.keys.Run(r.Context(), key, func(tx pgx.Tx) (idempotency.Response, error) {
		return do(h.as(r).WithTx(tx))
	})
	if err != nil {
		return err
	}
	resp.Write(w)
	return nil
}

func (h *Handler) createWallet(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Currency string `json:"currency"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	acc, err := h.svc.CreateWallet(r.Context(), caller(r.Context()).String(), req.Currency)
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
	acc, err := h.as(r).Wallet(r.Context(), id)
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
	b, err := h.as(r).Balance(r.Context(), id)
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
	page, err := h.as(r).Entries(r.Context(), id, r.URL.Query().Get("cursor"), limit)
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

func toQuote(q FXQuote) quoteResponse {
	return quoteResponse{
		ID:            q.ID,
		Sell:          Money{Amount: q.SellAmount, Currency: q.SellCurrency},
		Buy:           Money{Amount: q.BuyAmount, Currency: q.BuyCurrency},
		Fee:           Money{Amount: q.Fee, Currency: q.SellCurrency},
		Rate:          q.Rate,
		ExpiresAt:     q.ExpiresAt,
		TransactionID: q.TransactionID,
	}
}

func (h *Handler) quote(w http.ResponseWriter, r *http.Request) error {
	var req QuoteRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	quote, err := h.svc.Quote(r.Context(), caller(r.Context()), req)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, r, http.StatusCreated, toQuote(quote))
	return nil
}

// convert takes a quote. A retry with the same Idempotency-Key gets the same
// answer; a new request for a used quote gets quote_used.
func (h *Handler) convert(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		QuoteID uuid.UUID `json:"quote_id"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	return h.idempotent(w, r, req, func(svc *Service) (idempotency.Response, error) {
		used, err := svc.Convert(r.Context(), caller(r.Context()), req.QuoteID)
		if err != nil {
			return idempotency.Response{}, err
		}
		return idempotency.JSON(http.StatusCreated, toQuote(used))
	})
}

// RegisterInternal adds our finance team's reports to mux. The gateway never
// routes /internal/: only our own network can reach it.
func (h *Handler) RegisterInternal(mux *http.ServeMux) {
	mux.Handle("GET /internal/trial-balance.csv", httpx.HandlerFunc(h.trialBalance))
}

// statement sends a wallet's statement for the days from and to, as CSV. It
// streams: the first lines leave before the last ones are read.
func (h *Handler) statement(w http.ResponseWriter, r *http.Request) error {
	id, err := walletID(r)
	if err != nil {
		return err
	}
	from, to, err := period(r)
	if err != nil {
		return err
	}
	lines, err := h.as(r).Statement(r.Context(), id, from, to)
	if err != nil {
		return err
	}

	// The status goes out with the first bytes: from here on, an error can
	// only cut the download short.
	out := download(w, "statement-"+from.Format(time.DateOnly)+"-to-"+to.Format(time.DateOnly)+".csv")
	out.Write(StatementHeader)
	for line, err := range lines {
		var rec []string
		if err == nil {
			rec, err = line.Record()
		}
		if err == nil {
			err = out.Write(rec)
		}
		if err != nil {
			abort(r, err)
		}
	}
	out.Flush()
	if err := out.Error(); err != nil {
		abort(r, err)
	}
	return nil
}

// download starts sending a CSV file, called name.
func download(w http.ResponseWriter, name string) *csv.Writer {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store") // someone's money: never in a shared cache
	return csv.NewWriter(w)
}

// abort ends a response that has begun: the client sees a broken download,
// never a short file that looks complete.
func abort(r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "download aborted", "path", r.URL.Path, "err", err)
	panic(http.ErrAbortHandler)
}

// date reads a query parameter that's a day, like 2026-11-30, in UTC. A
// problem goes into v.
func date(r *http.Request, key string, v *apperr.ValidationError) time.Time {
	d, err := time.Parse(time.DateOnly, r.URL.Query().Get(key))
	if err != nil {
		v.Add(key, "must be a date, like 2026-11-30")
	}
	return d
}

// period reads a statement's first and last days, from and to, both included:
// a year at most.
func period(r *http.Request) (from, to time.Time, err error) {
	var v apperr.ValidationError
	from, to = date(r, "from", &v), date(r, "to", &v)
	if v.Err() == nil && (to.Before(from) || to.After(from.AddDate(1, 0, 0))) {
		v.Add("to", "must be on or after from, and at most a year later")
	}
	return from, to, v.Err()
}

// trialBalance sends finance the trial balance at the end of a day, as CSV:
// every ledger account's debits, credits and balance, and a total for each
// currency, whose balance must be zero.
func (h *Handler) trialBalance(w http.ResponseWriter, r *http.Request) error {
	var v apperr.ValidationError
	day := date(r, "date", &v)
	if err := v.Err(); err != nil {
		return err
	}
	lines, err := h.svc.TrialBalance(r.Context(), day.AddDate(0, 0, 1))
	if err != nil {
		return err
	}
	records := [][]string{{"currency", "account", "debits", "credits", "balance"}}
	for _, l := range lines {
		rec := []string{l.Currency, l.Account}
		for _, amount := range []int64{l.Debits, l.Credits, l.Debits - l.Credits} {
			s, err := fx.Format(amount, l.Currency)
			if err != nil {
				return err // nothing is sent yet: still a 500
			}
			rec = append(rec, s)
		}
		records = append(records, rec)
	}
	if err := download(w, "trial-balance-"+day.Format(time.DateOnly)+".csv").WriteAll(records); err != nil {
		abort(r, err)
	}
	return nil
}
