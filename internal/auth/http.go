package auth

import (
	"net/http"
	"time"

	"github.com/fiston-user/payments/internal/platform/authn"
	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/google/uuid"
)

// The API's JSON shapes.
type (
	credentials struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	refreshRequest struct {
		RefreshToken string `json:"refresh_token"`
	}
	userResponse struct {
		ID        uuid.UUID `json:"id"`
		Email     string    `json:"email"`
		CreatedAt time.Time `json:"created_at"`
	}
	// tokenResponse has the shape of an OAuth 2.0 token response, which
	// client libraries already know how to read.
	tokenResponse struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"` // seconds
		RefreshToken string `json:"refresh_token"`
	}
)

func toUserResponse(u User) userResponse {
	return userResponse{ID: u.ID, Email: u.Email, CreatedAt: u.CreatedAt}
}

// Handler serves the auth API over HTTP.
type Handler struct {
	svc      *Service
	verifier *authn.Verifier
}

// NewHandler returns a Handler that serves svc. verifier checks the access
// tokens of requests to protected routes.
func NewHandler(svc *Service, verifier *authn.Verifier) *Handler {
	return &Handler{svc: svc, verifier: verifier}
}

// Register adds the auth API's routes to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("POST /v1/auth/register", httpx.HandlerFunc(h.registerUser))
	mux.Handle("POST /v1/auth/login", httpx.HandlerFunc(h.login))
	mux.Handle("POST /v1/auth/refresh", httpx.HandlerFunc(h.refresh))
	mux.Handle("POST /v1/auth/logout", httpx.HandlerFunc(h.logout))
	mux.Handle("GET /v1/auth/me", h.verifier.Require(httpx.HandlerFunc(h.me)))
}

// RegisterInternal adds the routes for our own services to mux. The gateway
// never routes /internal/ to us.
func (h *Handler) RegisterInternal(mux *http.ServeMux) {
	mux.Handle("GET /internal/users/{id}", httpx.HandlerFunc(h.user))
}

func (h *Handler) registerUser(w http.ResponseWriter, r *http.Request) error {
	var req credentials
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	u, err := h.svc.Register(r.Context(), req.Email, req.Password)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, r, http.StatusCreated, toUserResponse(u))
	return nil
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) error {
	var req credentials
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	t, err := h.svc.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		return err
	}
	writeTokens(w, r, t)
	return nil
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) error {
	var req refreshRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	t, err := h.svc.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		return err
	}
	writeTokens(w, r, t)
	return nil
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) error {
	var req refreshRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	if err := h.svc.Logout(r.Context(), req.RefreshToken); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// me returns the user the access token belongs to. Require guarantees there
// is one.
func (h *Handler) me(w http.ResponseWriter, r *http.Request) error {
	id, _ := authn.UserID(r.Context())
	u, err := h.svc.User(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, r, http.StatusOK, toUserResponse(u))
	return nil
}

// user returns any user, by ID, for services like notification that only
// know IDs.
func (h *Handler) user(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return ErrUserNotFound
	}
	u, err := h.svc.User(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, r, http.StatusOK, toUserResponse(u))
	return nil
}

// writeTokens sends a pair of tokens. No browser or proxy may cache them.
func writeTokens(w http.ResponseWriter, r *http.Request, t Tokens) {
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, r, http.StatusOK, tokenResponse{
		AccessToken:  t.Access,
		TokenType:    "Bearer",
		ExpiresIn:    int(t.ExpiresIn.Seconds()),
		RefreshToken: t.Refresh,
	})
}
