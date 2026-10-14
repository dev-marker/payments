// Package webhook delivers events to our users' servers: signed, retried, and
// logged, so they never have to poll us.
package webhook

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/fiston-user/payments/internal/platform/apperr"
	"github.com/fiston-user/payments/internal/platform/authn"
	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/fiston-user/payments/internal/transfer/transferevents"
	"github.com/fiston-user/payments/internal/webhook/signature"
	"github.com/fiston-user/payments/internal/webhook/webhookdb"
	"github.com/google/uuid"
)

// EventTypes are the events an endpoint can subscribe to.
var EventTypes = []string{transferevents.Completed, transferevents.Failed}

var (
	ErrEndpointNotFound = fmt.Errorf("webhook endpoint %w", apperr.ErrNotFound)
	ErrDeliveryNotFound = fmt.Errorf("webhook delivery %w", apperr.ErrNotFound)
)

// API lets users manage their endpoints, and read and replay their deliveries.
type API struct {
	q          *webhookdb.Queries
	production bool
	wake       func() // tells the worker there's something to deliver
}

// NewAPI returns an API on q.
func NewAPI(q *webhookdb.Queries, production bool, wake func()) *API {
	return &API{q: q, production: production, wake: wake}
}

// Register adds the webhook routes to mux.
func (a *API) Register(mux *http.ServeMux) {
	mux.Handle("POST /v1/webhooks/endpoints", httpx.HandlerFunc(a.create))
	mux.Handle("GET /v1/webhooks/endpoints", httpx.HandlerFunc(a.list))
	mux.Handle("DELETE /v1/webhooks/endpoints/{id}", httpx.HandlerFunc(a.delete))
	mux.Handle("GET /v1/webhooks/deliveries", httpx.HandlerFunc(a.deliveries))
	mux.Handle("POST /v1/webhooks/deliveries/{id}/replay", httpx.HandlerFunc(a.replay))
}

func (a *API) create(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		URL        string   `json:"url"`
		EventTypes []string `json:"event_types"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	var v apperr.ValidationError
	if err := checkURL(req.URL, a.production); err != nil {
		v.Add("url", err.Error())
	}
	unknown := func(t string) bool { return !slices.Contains(EventTypes, t) }
	if len(req.EventTypes) == 0 || slices.ContainsFunc(req.EventTypes, unknown) {
		v.Add("event_types", "must list one or more of "+strings.Join(EventTypes, ", "))
	}
	if err := v.Err(); err != nil {
		return err
	}
	user, _ := authn.UserID(r.Context())
	endpoint, err := a.q.CreateEndpoint(r.Context(), webhookdb.CreateEndpointParams{
		ID: uuid.Must(uuid.NewV7()), UserID: user, URL: req.URL,
		EventTypes: req.EventTypes, Secret: signature.NewSecret(),
	})
	if err != nil {
		return err
	}
	// The one response with the secret in it: the user keeps it, to verify
	// what we send. Listing endpoints never shows it again.
	httpx.WriteJSON(w, r, http.StatusCreated, endpoint)
	return nil
}

func (a *API) list(w http.ResponseWriter, r *http.Request) error {
	user, _ := authn.UserID(r.Context())
	endpoints, err := a.q.ListEndpoints(r.Context(), user)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"endpoints": endpoints})
	return nil
}

func (a *API) delete(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return ErrEndpointNotFound
	}
	user, _ := authn.UserID(r.Context())
	n, err := a.q.DeleteEndpoint(r.Context(), webhookdb.DeleteEndpointParams{ID: id, UserID: user})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrEndpointNotFound // or someone else's: the same, to them
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
