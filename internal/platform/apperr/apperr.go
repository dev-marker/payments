// Package apperr defines the errors our services share. They describe what
// went wrong, not how to report it: HTTP, and later gRPC, map them to codes.
package apperr

import (
	"errors"
	"maps"
	"slices"
	"strings"
)

// Wrap these to add detail: fmt.Errorf("account %s: %w", id, apperr.ErrNotFound).
var (
	// ErrNotFound means the requested resource doesn't exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict means the request clashes with the current state, such as
	// creating something that already exists.
	ErrConflict = errors.New("conflict")
	// ErrUnauthenticated means the caller didn't prove who they are: a wrong
	// password, or a token that's missing, invalid, expired or revoked.
	ErrUnauthenticated = errors.New("unauthenticated")
)

// ValidationError reports which fields of a request are invalid, and why.
type ValidationError struct {
	Fields map[string]string
}

// Add records a problem with one field.
func (v *ValidationError) Add(field, problem string) {
	if v.Fields == nil {
		v.Fields = make(map[string]string)
	}
	v.Fields[field] = problem
}

// Err returns v if any field is invalid, and nil otherwise. Always return
// Err(), never v itself: a nil *ValidationError inside an error isn't nil.
func (v *ValidationError) Err() error {
	if len(v.Fields) == 0 {
		return nil
	}
	return v
}

func (v *ValidationError) Error() string {
	fields := slices.Sorted(maps.Keys(v.Fields))
	return "invalid input: " + strings.Join(fields, ", ")
}

// RuleError means a request was valid, but breaks a business rule, like
// spending more than a wallet holds. Code is a stable reason, like
// insufficient_funds, that clients can branch on.
type RuleError struct {
	Code    string
	Message string
}

// NewRule returns a RuleError. Declare one per rule as a package variable, and
// wrap it to add detail: fmt.Errorf("wallet %s: %w", id, ErrInsufficientFunds).
func NewRule(code, message string) *RuleError {
	return &RuleError{Code: code, Message: message}
}

func (e *RuleError) Error() string {
	return e.Message
}
