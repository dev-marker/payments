// Package config reads service configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Env reads typed values from environment variables. It doesn't stop at the
// first problem: it collects them all, so a misconfigured service reports
// everything that's wrong in one go.
type Env struct {
	lookup   func(key string) (string, bool)
	problems []string
}

// New returns an Env that reads variables with lookup, which is os.LookupEnv
// in a real service. Tests pass a function backed by a map.
func New(lookup func(key string) (string, bool)) *Env {
	return &Env{lookup: lookup}
}

// get returns a variable's value without surrounding spaces, and whether it
// has one. A variable that is set but empty counts as unset.
func (e *Env) get(key string) (string, bool) {
	v, _ := e.lookup(key)
	v = strings.TrimSpace(v)
	return v, v != ""
}

// Invalid records a problem with a variable.
func (e *Env) Invalid(key, problem string) {
	e.problems = append(e.problems, key+": "+problem)
}

// Err returns nil if every variable was valid. Otherwise it returns a single
// error that lists every problem, one per line.
func (e *Env) Err() error {
	if len(e.problems) == 0 {
		return nil
	}
	return errors.New("invalid configuration:\n  " + strings.Join(e.problems, "\n  "))
}

// String returns the variable's value, or def if it is unset.
func (e *Env) String(key, def string) string {
	if v, ok := e.get(key); ok {
		return v
	}
	return def
}

// Required returns the variable's value, and records a problem if it is unset.
func (e *Env) Required(key string) string {
	v, ok := e.get(key)
	if !ok {
		e.Invalid(key, "required, but not set")
	}
	return v
}

// OneOf returns the variable's value, or def if it is unset, and records a
// problem if the value isn't one of allowed.
func (e *Env) OneOf(key, def string, allowed ...string) string {
	v := e.String(key, def)
	if !slices.Contains(allowed, v) {
		e.Invalid(key, fmt.Sprintf("%q is not one of %s", v, strings.Join(allowed, ", ")))
	}
	return v
}

// Parse returns the variable converted by parse, or def if it is unset. A
// value that doesn't parse is recorded as a problem. It's a function, not a
// method, because Go methods can't have type parameters.
func Parse[T any](e *Env, key string, def T, parse func(string) (T, error)) T {
	v, ok := e.get(key)
	if !ok {
		return def
	}
	x, err := parse(v)
	if err != nil {
		e.Invalid(key, fmt.Sprintf("invalid value %q", v))
		return def
	}
	return x
}

// Int returns the variable as a whole number, or def if it is unset.
func (e *Env) Int(key string, def int) int {
	return Parse(e, key, def, strconv.Atoi)
}

// Duration returns the variable as a duration such as 5s or 1m30s, or def if
// it is unset.
func (e *Env) Duration(key string, def time.Duration) time.Duration {
	return Parse(e, key, def, time.ParseDuration)
}
