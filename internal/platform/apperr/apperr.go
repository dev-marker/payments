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
