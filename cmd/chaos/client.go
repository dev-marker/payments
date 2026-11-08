package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// A client calls our API the way a careful client should: every write has an
// idempotency key, and a write without a clear answer is sent again, with the
// same key, until it gets one.
type client struct {
	base    string
	http    *http.Client
	retries atomic.Int64 // every extra try, of every request
}

func newClient(base string) *client {
	// A request with no answer after five seconds is lost: it may or may not
	// have happened. Only a retry, with the same key, can find out which.
	return &client{base: base, http: &http.Client{Timeout: 5 * time.Second}}
}

// An apiError is an answer that isn't a success: its status, and our code.
type apiError struct {
	Status int
	Code   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%d %s", e.Status, e.Code)
}

// final reports whether err is an answer that asking again won't change.
func final(err error) bool {
	e, ok := errors.AsType[*apiError](err)
	if !ok {
		return err == nil // no answer at all: try again
	}
	switch {
	case e.Status >= 500, e.Status == http.StatusTooManyRequests:
		return false // our fault, or slow down: try again
	case e.Code == "request_in_progress":
		return false // the first try is still running
	}
	return true
}

// A request is one call to the API: as whom, with which key, and what.
type request struct {
	path  string
	token string
	key   string
	body  any
}

// post sends r once, and decodes a successful answer into out. replayed says
// whether the API answered from its record of an earlier try.
func (c *client) post(ctx context.Context, r request, out any) (replayed bool, err error) {
	body, err := json.Marshal(r.body)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+r.path, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	if r.key != "" {
		req.Header.Set("Idempotency-Key", r.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct{ Code string } `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e) // a proxy's error may have no body
		return false, &apiError{resp.StatusCode, e.Error.Code}
	}
	return resp.Header.Get("Idempotent-Replayed") == "true", json.NewDecoder(resp.Body).Decode(out)
}

// send posts r until the answer is final, or ctx is done, always with the
// same key: the client's half of exactly once.
func (c *client) send(ctx context.Context, r request, out any) (replayed bool, err error) {
	wait := 250 * time.Millisecond
	for {
		replayed, err = c.post(ctx, r, out)
		if final(err) || ctx.Err() != nil {
			return replayed, err
		}
		c.retries.Add(1)
		// A random wait, up to a limit that doubles: clients that failed
		// together don't all come back at the same moment.
		select {
		case <-ctx.Done():
			return false, err
		case <-time.After(rand.N(wait)):
		}
		wait = min(2*wait, 5*time.Second)
	}
}

// A customer is a user who signed in, and their wallet.
type customer struct {
	token  string
	wallet uuid.UUID
}

// signUp registers a user, signs in, opens a wallet in currency, and deposits
// amount into it, unless it's zero.
func (c *client) signUp(ctx context.Context, email, currency string, amount int64) (customer, error) {
	creds := map[string]string{"email": email, "password": "correct horse battery staple"}
	var login struct {
		Token string `json:"access_token"`
	}
	var wallet struct {
		ID uuid.UUID `json:"id"`
	}
	err := c.call(ctx, request{path: "/v1/auth/register", body: creds}, nil)
	if err == nil {
		err = c.call(ctx, request{path: "/v1/auth/login", body: creds}, &login)
	}
	if err == nil {
		body := map[string]string{"currency": currency}
		err = c.call(ctx, request{path: "/v1/wallets", token: login.Token, body: body}, &wallet)
	}
	if err == nil && amount > 0 {
		err = c.call(ctx, request{
			path:  "/v1/wallets/" + wallet.ID.String() + "/deposits",
			token: login.Token,
			body:  map[string]any{"amount": amount, "currency": currency},
		}, nil)
	}
	if err != nil {
		return customer{}, fmt.Errorf("sign up %s: %w", email, err)
	}
	return customer{token: login.Token, wallet: wallet.ID}, nil
}

// call sends r with a new idempotency key, and decodes the answer into out,
// unless out is nil.
func (c *client) call(ctx context.Context, r request, out any) error {
	if out == nil {
		out = &struct{}{}
	}
	r.key = uuid.NewString()
	_, err := c.send(ctx, r, out)
	return err
}
