package webhook

import (
	"net/http"

	"github.com/fiston-user/payments/internal/platform/apperr"
	"github.com/fiston-user/payments/internal/platform/authn"
	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/fiston-user/payments/internal/webhook/webhookdb"
	"github.com/google/uuid"
)

// deliveries is the delivery log of one endpoint, newest first.
func (a *API) deliveries(w http.ResponseWriter, r *http.Request) error {
	endpoint, err := uuid.Parse(r.URL.Query().Get("endpoint"))
	if err != nil {
		var v apperr.ValidationError
		v.Add("endpoint", "must be the ID of one of your endpoints")
		return v.Err()
	}
	user, _ := authn.UserID(r.Context())
	log, err := a.q.ListDeliveries(r.Context(), webhookdb.ListDeliveriesParams{
		UserID: user, EndpointID: endpoint,
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"deliveries": log})
	return nil
}

// replay sends a delivery again, whatever happened to it before.
func (a *API) replay(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return ErrDeliveryNotFound
	}
	user, _ := authn.UserID(r.Context())
	n, err := a.q.Replay(r.Context(), webhookdb.ReplayParams{ID: id, UserID: user})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrDeliveryNotFound
	}
	a.wake()
	httpx.WriteJSON(w, r, http.StatusAccepted, map[string]string{"id": id.String(), "state": "pending"})
	return nil
}
