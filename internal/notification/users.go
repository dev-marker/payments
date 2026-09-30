package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"
)

// Users knows where to reach a user. Events carry user IDs, never addresses:
// an address can change, and events stay in the stream for a month.
type Users interface {
	Email(ctx context.Context, id uuid.UUID) (string, error)
}

// AuthUsers asks the auth service, which owns users.
type AuthUsers struct {
	BaseURL string // like http://localhost:8082
	Client  *http.Client
}

func (a AuthUsers) Email(ctx context.Context, id uuid.UUID) (string, error) {
	url := a.BaseURL + "/internal/users/" + id.String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("auth: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", Permanent(fmt.Errorf("user %s not found", id))
	default:
		return "", fmt.Errorf("auth: %s", resp.Status)
	}
	var user struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", fmt.Errorf("auth: decode user: %w", err)
	}
	return user.Email, nil
}
