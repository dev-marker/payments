package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"time"

	"github.com/fiston-user/payments/internal/platform/authn"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Signer issues access tokens: short-lived JWTs, signed with the auth
// service's Ed25519 private key. No other service ever has that key.
type Signer struct {
	key   ed25519.PrivateKey
	keyID string
	ttl   time.Duration
}

// NewSigner returns a Signer whose tokens are valid for ttl.
func NewSigner(key ed25519.PrivateKey, ttl time.Duration) *Signer {
	return &Signer{key: key, keyID: authn.KeyID(key.Public().(ed25519.PublicKey)), ttl: ttl}
}

// PublicKey returns the key that verifies the Signer's tokens.
func (s *Signer) PublicKey() ed25519.PublicKey {
	return s.key.Public().(ed25519.PublicKey)
}

// Issue returns an access token for the user, with their roles, issued at
// now. Taking the time as an argument lets tests issue tokens that have
// already expired.
func (s *Signer) Issue(userID uuid.UUID, now time.Time, roles ...string) (string, error) {
	claims := jwt.RegisteredClaims{
		Issuer:    authn.Issuer,
		Subject:   userID.String(),
		Audience:  jwt.ClaimStrings{authn.Audience},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
		ID:        uuid.NewString(), // unique per token, for logs and audits
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, authn.Claims{RegisteredClaims: claims, Roles: roles})
	token.Header["kid"] = s.keyID
	signed, err := token.SignedString(s.key)
	if err != nil {
		return "", fmt.Errorf("sign access token: %w", err)
	}
	return signed, nil
}

// ReadPrivateKey reads an Ed25519 private key from a PEM file, in the PKCS #8
// format that openssl genpkey writes.
func ReadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("%s: no PEM private key", path)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	ed, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: a %T, not an Ed25519 key", path, key)
	}
	return ed, nil
}

// newRefreshToken returns a refresh token: 32 random bytes, which is 256
// bits that nobody can guess, as 43 URL-safe characters.
func newRefreshToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// hashToken is how a refresh token is stored and looked up. Unlike a
// password, the token is random and long, so a fast hash is enough: there's
// nothing to guess, even with the hash in hand.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
