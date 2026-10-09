package postgres

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // the pgx5:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// Migrate applies the migrations that the database at dsn doesn't have yet,
// and returns its version. Many instances can run it at once: golang-migrate
// holds a Postgres advisory lock while it works, so only one of them does.
func Migrate(dsn string, migrations fs.FS) (uint, error) {
	src, err := iofs.New(migrations, ".")
	if err != nil {
		return 0, fmt.Errorf("migrate: read migrations: %w", err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return 0, fmt.Errorf("migrate: parse dsn: %w", err)
	}
	u.Scheme = "pgx5" // golang-migrate picks its driver by the URL's scheme
	m, err := migrate.NewWithSourceInstance("iofs", src, u.String())
	if err != nil {
		return 0, fmt.Errorf("migrate: %w", err)
	}
	defer m.Close()

	// Up is a no-op when there's nothing new: running it twice is safe.
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return 0, fmt.Errorf("migrate: %w", err)
	}
	version, _, err := m.Version()
	return version, err
}
