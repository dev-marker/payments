package auth_test

import (
	"crypto/ed25519"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fiston-user/payments/internal/auth"
	"github.com/fiston-user/payments/internal/platform/authn"
	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/google/uuid"
)

// newService returns a Service on the database in AUTH_TEST_DATABASE_URL,
// with a new key pair, and a verifier for its access tokens. Without the
// database the test is skipped.
func newService(t *testing.T) (*auth.Service, *authn.Verifier) {
	t.Helper()
	dsn := os.Getenv("AUTH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AUTH_TEST_DATABASE_URL not set, skipping integration test")
	}
	pool, err := postgres.NewPool(t.Context(), dsn, 4)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	pub, key, _ := ed25519.GenerateKey(nil)
	svc := auth.NewService(pool, auth.NewSigner(key, 15*time.Minute), 24*time.Hour)
	return svc, authn.NewVerifier(pub)
}

// newUser registers a user with a new email address, and returns it.
func newUser(t *testing.T, svc *auth.Service) string {
	t.Helper()
	email := uuid.NewString() + "@example.com"
	if _, err := svc.Register(t.Context(), email, password); err != nil {
		t.Fatalf("register: %v", err)
	}
	return email
}

// login logs the user in, and returns the tokens.
func login(t *testing.T, svc *auth.Service, email string) auth.Tokens {
	t.Helper()
	tokens, err := svc.Login(t.Context(), email, password)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return tokens
}

func TestLogin(t *testing.T) {
	t.Parallel()
	svc, verifier := newService(t)
	email := newUser(t, svc)

	tests := []struct {
		name, email, password string
		err                   error
	}{
		{"right password", email, password, nil},
		{"email in capitals", strings.ToUpper(email), password, nil},
		{"wrong password", email, password + "s", auth.ErrInvalidCredentials},
		{"unknown email", "nobody." + email, password, auth.ErrInvalidCredentials},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens, err := svc.Login(t.Context(), tt.email, tt.password)
			if !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}
			if err == nil {
				if _, err := verifier.Verify(tokens.Access); err != nil {
					t.Errorf("access token: %v", err)
				}
			}
		})
	}
}

func TestRefreshRotation(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t)
	first := login(t, svc, newUser(t, svc))

	// Each refresh returns a new refresh token, and the one before it stops
	// working, without harm to the rest of the chain.
	second, err := svc.Refresh(t.Context(), first.Refresh)
	if err != nil || second.Refresh == first.Refresh {
		t.Fatalf("refresh: %v, or the refresh token wasn't rotated", err)
	}
	third, err := svc.Refresh(t.Context(), second.Refresh)
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}

	// Logging out ends the chain.
	if err := svc.Logout(t.Context(), third.Refresh); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := svc.Refresh(t.Context(), third.Refresh); !errors.Is(err, auth.ErrInvalidRefreshToken) {
		t.Errorf("refresh after logout: err = %v, want ErrInvalidRefreshToken", err)
	}
}

func TestRefreshReuse(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t)
	email := newUser(t, svc)
	stolen := login(t, svc, email)
	laptop := login(t, svc, email) // another session of the same user

	// The real client refreshes. Then the thief tries the old token.
	current, err := svc.Refresh(t.Context(), stolen.Refresh)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, err := svc.Refresh(t.Context(), stolen.Refresh); !errors.Is(err, auth.ErrInvalidRefreshToken) {
		t.Fatalf("reused token: err = %v, want ErrInvalidRefreshToken", err)
	}

	// The reuse revoked the whole family, so the current token is dead too.
	if _, err := svc.Refresh(t.Context(), current.Refresh); !errors.Is(err, auth.ErrInvalidRefreshToken) {
		t.Errorf("current token after reuse: err = %v, want ErrInvalidRefreshToken", err)
	}
	// But only that family: the laptop's session is fine.
	if _, err := svc.Refresh(t.Context(), laptop.Refresh); err != nil {
		t.Errorf("other session: %v", err)
	}
}
