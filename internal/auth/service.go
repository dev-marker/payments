// Package auth is the auth service: users, passwords, and the tokens that
// prove who a request comes from.
package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fiston-user/payments/internal/auth/authdb"
	"github.com/fiston-user/payments/internal/platform/apperr"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors clients see. Login and refresh failures say as little as possible.
var (
	ErrEmailTaken          = fmt.Errorf("%w: email already registered", apperr.ErrConflict)
	ErrInvalidCredentials  = fmt.Errorf("%w: invalid email or password", apperr.ErrUnauthenticated)
	ErrInvalidRefreshToken = fmt.Errorf("%w: invalid refresh token", apperr.ErrUnauthenticated)
	ErrUserNotFound        = fmt.Errorf("user %w", apperr.ErrNotFound)
)

// User is a registered user. It has no password hash on purpose: nothing
// outside this package ever needs it.
type User struct {
	ID        uuid.UUID
	Email     string
	CreatedAt time.Time
}

func toUser(u authdb.User) User {
	return User{ID: u.ID, Email: u.Email, CreatedAt: u.CreatedAt}
}

// Tokens are what a login or a refresh returns.
type Tokens struct {
	Access    string        // a JWT, for every request
	ExpiresIn time.Duration // how long Access lasts
	Refresh   string        // opaque, used once, to get the next Tokens
}

// Service is the auth service's business logic. Its queries are simple, so it
// uses the generated code directly, without a store in between.
type Service struct {
	pool       *pgxpool.Pool
	q          *authdb.Queries
	signer     *Signer
	refreshTTL time.Duration
	dummyHash  string
}

// NewService returns a Service that keeps users and refresh tokens in pool's
// database, and signs access tokens with signer.
func NewService(pool *pgxpool.Pool, signer *Signer, refreshTTL time.Duration) *Service {
	// What Login checks a password against when the email isn't registered.
	// Its password is random, and thrown away.
	dummy, _ := HashPassword(context.Background(), rand.Text()) // only fails if canceled
	return &Service{
		pool:       pool,
		q:          authdb.New(pool),
		signer:     signer,
		refreshTTL: refreshTTL,
		dummyHash:  dummy,
	}
}

// Register creates a user. Emails are unique without regard to case.
func (s *Service) Register(ctx context.Context, email, password string) (User, error) {
	email = strings.TrimSpace(email)
	var v apperr.ValidationError
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email || len(email) > 254 {
		v.Add("email", "must be an email address, like alice@example.com")
	}
	// Length is what makes a password strong. No rules about digits or symbols.
	if n := utf8.RuneCountInString(password); n < 12 || n > 128 {
		v.Add("password", "must be 12 to 128 characters long")
	}
	if err := v.Err(); err != nil {
		return User{}, err
	}

	hash, err := HashPassword(ctx, password)
	if err != nil {
		return User{}, err
	}
	u, err := s.q.CreateUser(ctx, authdb.CreateUserParams{Email: email, PasswordHash: hash})
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" {
		return User{}, ErrEmailTaken // unique_violation
	}
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return toUser(u), nil
}

// Login checks a user's email and password, and starts a session: a new
// refresh token family. A wrong password and an unknown email look exactly
// the same, so nobody can find out who has an account.
func (s *Service) Login(ctx context.Context, email, password string) (Tokens, error) {
	u, err := s.q.GetUserByEmail(ctx, strings.TrimSpace(email))
	if errors.Is(err, pgx.ErrNoRows) {
		// Hash anyway, so this takes as long as a wrong password does.
		VerifyPassword(ctx, password, s.dummyHash)
		return Tokens{}, ErrInvalidCredentials
	}
	if err != nil {
		return Tokens{}, fmt.Errorf("get user: %w", err)
	}
	ok, err := VerifyPassword(ctx, password, u.PasswordHash)
	if err != nil {
		return Tokens{}, err
	}
	if !ok {
		return Tokens{}, ErrInvalidCredentials
	}
	return s.issue(ctx, s.q, u.ID, uuid.New(), nil)
}

// issue makes a new pair of tokens for a user: an access token, and a refresh
// token in family. parent is the refresh token it replaces, or nil.
func (s *Service) issue(
	ctx context.Context, q *authdb.Queries, userID, family uuid.UUID, parent *uuid.UUID,
) (Tokens, error) {
	now := time.Now()
	access, err := s.signer.Issue(userID, now)
	if err != nil {
		return Tokens{}, err
	}
	refresh := newRefreshToken()
	err = q.CreateRefreshToken(ctx, authdb.CreateRefreshTokenParams{
		UserID:    userID,
		TokenHash: hashToken(refresh),
		FamilyID:  family,
		ParentID:  parent,
		ExpiresAt: now.Add(s.refreshTTL),
	})
	if err != nil {
		return Tokens{}, fmt.Errorf("store refresh token: %w", err)
	}
	return Tokens{Access: access, ExpiresIn: s.signer.ttl, Refresh: refresh}, nil
}

// Refresh trades a refresh token for a new pair, and retires the old one, so
// each refresh token works once. If a retired token comes back, two parties
// hold copies of it, and we can't tell which one is the thief: we revoke its
// whole family, and the user logs in again.
func (s *Service) Refresh(ctx context.Context, token string) (Tokens, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Tokens{}, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)

	// The row lock makes two refreshes with the same token take turns: the
	// second one sees that the first one rotated it.
	old, err := q.GetRefreshTokenForUpdate(ctx, hashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return Tokens{}, ErrInvalidRefreshToken
	}
	if err != nil {
		return Tokens{}, fmt.Errorf("get refresh token: %w", err)
	}

	switch {
	case old.RevokedAt.Valid || !old.ExpiresAt.After(time.Now()):
		return Tokens{}, ErrInvalidRefreshToken
	case old.RotatedAt.Valid:
		// Reuse. Revoke the family, and commit that, even though we fail.
		n, err := q.RevokeFamily(ctx, old.FamilyID)
		if err != nil {
			return Tokens{}, fmt.Errorf("revoke family: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return Tokens{}, fmt.Errorf("commit: %w", err)
		}
		slog.WarnContext(ctx, "refresh token reused, family revoked",
			"user_id", old.UserID, "family_id", old.FamilyID, "revoked", n)
		return Tokens{}, ErrInvalidRefreshToken
	}

	// Rotate: retire the old token, and issue its successor in the same family.
	if err := q.RotateRefreshToken(ctx, old.ID); err != nil {
		return Tokens{}, fmt.Errorf("rotate refresh token: %w", err)
	}
	tokens, err := s.issue(ctx, q, old.UserID, old.FamilyID, &old.ID)
	if err != nil {
		return Tokens{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Tokens{}, fmt.Errorf("commit: %w", err)
	}
	return tokens, nil
}

// Logout ends the session that token belongs to, by revoking its family. An
// unknown token is not an error: the session is over either way. Access
// tokens already issued keep working until they expire.
func (s *Service) Logout(ctx context.Context, token string) error {
	if _, err := s.q.RevokeFamilyOf(ctx, hashToken(token)); err != nil {
		return fmt.Errorf("revoke family: %w", err)
	}
	return nil
}

// User returns the user with the given ID.
func (s *Service) User(ctx context.Context, id uuid.UUID) (User, error) {
	u, err := s.q.GetUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("get user: %w", err)
	}
	return toUser(u), nil
}
