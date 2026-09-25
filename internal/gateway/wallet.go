package gateway

import (
	"context"
	"net/http"
	"time"

	walletv1 "github.com/fiston-user/payments/gen/wallet/v1"
	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/fiston-user/payments/internal/platform/idempotency"
	"github.com/fiston-user/payments/internal/wallet/walletclient"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// The public JSON, as the wallet's REST API has always sent it.
type (
	money struct {
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	}
	walletJSON struct {
		ID        string    `json:"id"`
		OwnerID   string    `json:"owner_id"`
		Currency  string    `json:"currency"`
		CreatedAt time.Time `json:"created_at"`
	}
	balanceJSON struct {
		WalletID string `json:"wallet_id"`
		Balance  money  `json:"balance"`
	}
	depositJSON struct {
		TransactionID string    `json:"transaction_id"`
		WalletID      string    `json:"wallet_id"`
		Amount        money     `json:"amount"`
		CreatedAt     time.Time `json:"created_at"`
	}
)

func (m money) pb() *walletv1.Money {
	return &walletv1.Money{AmountMinor: m.Amount, Currency: m.Currency}
}

func toMoney(m *walletv1.Money) money {
	return money{Amount: m.GetAmountMinor(), Currency: m.GetCurrency()}
}

func toWallet(w *walletv1.Wallet) walletJSON {
	return walletJSON{ID: w.GetId(), OwnerID: w.GetOwnerId(), Currency: w.GetCurrency(),
		CreatedAt: w.GetCreateTime().AsTime()}
}

// walletAPI serves the wallet's routes over gRPC. It only translates.
type walletAPI struct {
	client *walletclient.Client
}

// rpc sends a handler's errors through fromRPC.
func rpc(fn httpx.HandlerFunc) httpx.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		return fromRPC(fn(w, r))
	}
}

// asCaller sends the user's own token along, for the service to check again.
func asCaller(r *http.Request) context.Context {
	return metadata.AppendToOutgoingContext(r.Context(), "authorization", r.Header.Get("Authorization"))
}

func (a *walletAPI) create(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Currency string `json:"currency"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	resp, err := a.client.CreateWallet(asCaller(r), &walletv1.CreateWalletRequest{Currency: req.Currency})
	if err != nil {
		return err
	}
	wallet := toWallet(resp.GetWallet())
	w.Header().Set("Location", "/v1/wallets/"+wallet.ID)
	httpx.WriteJSON(w, r, http.StatusCreated, wallet)
	return nil
}

func (a *walletAPI) get(w http.ResponseWriter, r *http.Request) error {
	resp, err := a.client.GetWallet(asCaller(r), &walletv1.GetWalletRequest{WalletId: r.PathValue("id")})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, r, http.StatusOK, toWallet(resp.GetWallet()))
	return nil
}

func (a *walletAPI) balance(w http.ResponseWriter, r *http.Request) error {
	resp, err := a.client.GetBalance(asCaller(r), &walletv1.GetBalanceRequest{WalletId: r.PathValue("id")})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, r, http.StatusOK, balanceJSON{
		WalletID: resp.GetWalletId(),
		Balance:  toMoney(resp.GetBalance()),
	})
	return nil
}

func (a *walletAPI) deposit(w http.ResponseWriter, r *http.Request) error {
	var amount money
	if err := httpx.DecodeJSON(w, r, &amount); err != nil {
		return err
	}
	var header metadata.MD
	resp, err := a.client.Deposit(asCaller(r), &walletv1.DepositRequest{
		IdempotencyKey: r.Header.Get(idempotency.KeyHeader),
		WalletId:       r.PathValue("id"),
		Amount:         amount.pb(),
	}, grpc.Header(&header))
	if err != nil {
		return err
	}
	replayed(w, header)
	httpx.WriteJSON(w, r, http.StatusCreated, depositJSON{
		TransactionID: resp.GetTransactionId(),
		WalletID:      resp.GetWalletId(),
		Amount:        toMoney(resp.GetAmount()),
		CreatedAt:     resp.GetCreateTime().AsTime(),
	})
	return nil
}

// replayed copies the wallet's replay flag into Idempotent-Replayed.
func replayed(w http.ResponseWriter, header metadata.MD) {
	if v := header.Get("idempotent-replayed"); len(v) > 0 {
		w.Header().Set(idempotency.ReplayedHeader, v[0])
	}
}
