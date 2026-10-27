package wallet_test

import (
	"errors"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestRuntimeRole connects as wallet_app, the role the wallet runs as, and
// tries what a stolen password would: rewrite the ledger, switch its
// triggers off, or change the schema. Postgres must refuse every one.
func TestRuntimeRole(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration test")
	}
	u, err := url.Parse(postgresURL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("wallet_app", "wallet_app")
	conn, err := pgx.Connect(t.Context(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	if _, err := conn.Exec(t.Context(), "SELECT count(*) FROM entries"); err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	for _, sql := range []string{
		"UPDATE entries SET amount = amount + 1000",
		"DELETE FROM transactions",
		"SET session_replication_role = replica",
		"CREATE TABLE backdoor (id int)",
	} {
		_, err := conn.Exec(t.Context(), sql)
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "42501" {
			t.Errorf("%s: %v, want insufficient_privilege", sql, err)
		}
	}
}
