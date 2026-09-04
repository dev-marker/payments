package postgres_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRetry(t *testing.T) {
	t.Parallel()
	conflict := &pgconn.PgError{Code: "40001"} // serialization_failure
	deadlock := &pgconn.PgError{Code: "40P01"} // deadlock_detected
	broken := errors.New("connection refused")

	tests := []struct {
		name  string
		errs  []error // what each attempt returns; then nil
		calls int
		ok    bool
	}{
		{"works the first time", nil, 1, true},
		{"retries a serialization failure", []error{conflict}, 2, true},
		{"retries a wrapped deadlock", []error{fmt.Errorf("commit: %w", deadlock), deadlock}, 3, true},
		{"gives up eventually", slices.Repeat([]error{conflict}, 10), postgres.MaxAttempts, false},
		{"never retries other errors", []error{broken}, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			err := postgres.Retry(t.Context(), func() error {
				calls++
				if calls <= len(tt.errs) {
					return tt.errs[calls-1]
				}
				return nil
			})
			if calls != tt.calls || (err == nil) != tt.ok {
				t.Errorf("%d calls, error %v; want %d calls, ok = %t", calls, err, tt.calls, tt.ok)
			}
		})
	}
}
