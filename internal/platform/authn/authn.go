// Package authn checks who sent a request. The auth service issues access
// tokens: JWTs signed with its Ed25519 private key. Any service can verify
// them with the public key alone, without calling the auth service.
package authn

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fiston-user/payments/internal/platform/apperr"
	"github.com/fiston-user/payments/internal/platform/httpx"
	"github.com/fiston-user/payments/internal/platform/logging"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Who issues access tokens, and who they're for. A token for another issuer
// or audience is refused, even with a valid signature.
const (
	Issuer   = "payments-auth"
	Audience = "payments-api"
)

// Errors a client sees. Both are a 401.
var (
	ErrMissingToken = fmt.Errorf("%w: missing bearer token", apperr.ErrUnauthenticated)
	ErrInvalidToken = fmt.Errorf("%w: invalid or expired access token", apperr.ErrUnauthenticated)
)

// KeyID names a public key: the start of its SHA-256. Tokens carry it in their
// header, so after a key rotation, a verifier knows which key signed them.
func KeyID(key ed25519.PublicKey) string {
	sum := sha256.Sum256(key)
	return base64.RawURLEncoding.EncodeToString(sum[:8])
}

// Verifier checks access tokens.
type Verifier struct {
	key    ed25519.PublicKey
	keyID  string
	parser *jwt.Parser
}

// NewVerifier returns a Verifier for tokens signed with key's private half.
func NewVerifier(key ed25519.PublicKey) *Verifier {
	return &Verifier{
		key:   key,
		keyID: KeyID(key),
		parser: jwt.NewParser(
			// Only EdDSA. The token's header names its algorithm, and a
			// verifier that believes it can be tricked into accepting none.
			jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
			jwt.WithIssuer(Issuer),
			jwt.WithAudience(Audience),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
			jwt.WithLeeway(30*time.Second), // clocks on different servers drift
		),
	}
}

// Verify checks token's signature and claims, and returns the ID of the user
// it was issued to. Any problem is an ErrInvalidToken, with the details.
func (v *Verifier) Verify(token string) (uuid.UUID, error) {
	var claims jwt.RegisteredClaims
	_, err := v.parser.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if kid, _ := t.Header["kid"].(string); kid != v.keyID {
			return nil, fmt.Errorf("unknown key ID %q", kid)
		}
		return v.key, nil
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: subject %q is not a user ID", ErrInvalidToken, claims.Subject)
	}
	return id, nil
}

type userIDKey struct{}

// Require is middleware that only lets requests with a valid access token
// through, sent as Authorization: Bearer <token>. Handlers behind it get the
// caller's ID from UserID.
func (v *Verifier) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r.Header.Get("Authorization"))
		if token == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			httpx.Error(w, r, ErrMissingToken)
			return
		}
		id, err := v.Verify(token)
		if err != nil {
			// The client only learns that the token is no good. The log says why.
			slog.InfoContext(r.Context(), "access token refused", "err", err)
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			httpx.Error(w, r, ErrInvalidToken)
			return
		}
		next.ServeHTTP(w, r.WithContext(withUser(r.Context(), id)))
	})
}

// bearer returns the token in an Authorization value, "Bearer <token>", or "".
func bearer(value string) string {
	scheme, token, _ := strings.Cut(value, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return token
}

// withUser returns a copy of ctx that carries the caller's ID, for UserID,
// and for every line logged with it.
func withUser(ctx context.Context, id uuid.UUID) context.Context {
	ctx = context.WithValue(ctx, userIDKey{}, id)
	return logging.With(ctx, slog.String("user_id", id.String()))
}

// UserID returns the ID of the user who sent the request, as Require stored
// it in ctx.
func UserID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userIDKey{}).(uuid.UUID)
	return id, ok
}

// ReadPublicKey reads an Ed25519 public key from a PEM file, in the PKIX
// format that openssl pkey -pubout writes.
func ReadPublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("%s: no PEM public key", path)
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	ed, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%s: a %T, not an Ed25519 key", path, key)
	}
	return ed, nil
}
