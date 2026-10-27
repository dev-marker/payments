package auth_test

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fiston-user/payments/internal/auth"
	"github.com/fiston-user/payments/internal/platform/authn"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestAccessToken(t *testing.T) {
	t.Parallel()
	pub, key, _ := ed25519.GenerateKey(nil)
	_, otherKey, _ := ed25519.GenerateKey(nil)
	signer := auth.NewSigner(key, 15*time.Minute)
	verifier := authn.NewVerifier(pub)
	user, now := uuid.New(), time.Now()

	issue := func(s *auth.Signer, user uuid.UUID, at time.Time) string {
		token, err := s.Issue(user, at)
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		return token
	}
	valid := issue(signer, user, now)

	// forge signs claims the way an attacker would: with any algorithm and
	// any key, and our key's ID in the header.
	forge := func(method jwt.SigningMethod, key any, edit func(*jwt.RegisteredClaims)) string {
		claims := jwt.RegisteredClaims{
			Issuer:    authn.Issuer,
			Subject:   user.String(),
			Audience:  jwt.ClaimStrings{authn.Audience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
		}
		if edit != nil {
			edit(&claims)
		}
		token := jwt.NewWithClaims(method, claims)
		token.Header["kid"] = authn.KeyID(pub)
		s, err := token.SignedString(key)
		if err != nil {
			t.Fatalf("forge: %v", err)
		}
		return s
	}

	// Another user's header and claims, with the valid token's signature.
	other := strings.Split(issue(signer, uuid.New(), now), ".")
	swapped := other[0] + "." + other[1] + "." + strings.Split(valid, ".")[2]

	tests := []struct {
		name  string
		token string
		ok    bool
	}{
		{"valid", valid, true},
		{"expired", issue(signer, user, now.Add(-20*time.Minute)), false},
		{"claims swapped", swapped, false},
		{"signed with another key", forge(jwt.SigningMethodEdDSA, otherKey, nil), false},
		{"alg none", forge(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, nil), false},
		{"HS256, public key as secret", forge(jwt.SigningMethodHS256, []byte(pub), nil), false},
		{"other audience", forge(jwt.SigningMethodEdDSA, key, func(c *jwt.RegisteredClaims) {
			c.Audience = jwt.ClaimStrings{"someone-else"}
		}), false},
		{"no expiry", forge(jwt.SigningMethodEdDSA, key, func(c *jwt.RegisteredClaims) {
			c.ExpiresAt = nil
		}), false},
		{"not a JWT", "hello", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := verifier.Verify(tt.token)
			switch {
			case tt.ok && (err != nil || got.ID != user):
				t.Errorf("Verify = %s, %v; want %s", got, err, user)
			case !tt.ok && !errors.Is(err, authn.ErrInvalidToken):
				t.Errorf("Verify = %s, %v; want ErrInvalidToken", got, err)
			}
		})
	}
}
