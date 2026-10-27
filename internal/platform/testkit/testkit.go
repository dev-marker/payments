// Package testkit runs the real infrastructure that integration tests need,
// in throwaway containers: Postgres with a service's migrations, NATS with
// JetStream, and Redis. A test package starts what it needs once, in its
// TestMain, and its tests share it.
package testkit

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/fiston-user/payments/db"
	pg "github.com/fiston-user/payments/internal/platform/postgres"
	"github.com/testcontainers/testcontainers-go"
	tclog "github.com/testcontainers/testcontainers-go/log"
	"github.com/testcontainers/testcontainers-go/modules/nats"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/modules/redis"
)

// The images in docker-compose.yml: tests run on what we run.
const (
	postgresImage = "postgres:17-alpine"
	natsImage     = "nats:2.11-alpine"
	redisImage    = "redis:8-alpine"
)

// A Kit starts containers for one test package, and keeps track of them.
// After an error, it starts nothing more, and Main reports the error.
type Kit struct {
	ctx        context.Context
	containers []testcontainers.Container
	err        error
}

// Main runs a test package's tests. start starts the containers they need,
// which are removed once the tests are done. With -short, nothing starts:
// integration tests skip themselves, and the unit tests run alone.
func Main(m *testing.M, start func(*Kit)) {
	flag.Parse() // for testing.Short
	if testing.Short() {
		os.Exit(m.Run())
	}
	if !testing.Verbose() {
		tclog.SetDefault(tclog.NewNoopLogger()) // only -v shows the containers
	}
	k := &Kit{ctx: context.Background()}
	start(k)
	code := 1
	if k.err != nil {
		fmt.Fprintln(os.Stderr, "testkit:", k.err)
	} else {
		code = m.Run()
	}
	for _, c := range k.containers {
		testcontainers.TerminateContainer(c)
	}
	os.Exit(code)
}

// keep tracks a container, to remove it later, and the error starting it.
func (k *Kit) keep(c testcontainers.Container, err error) bool {
	k.containers = append(k.containers, c)
	k.err = err
	return err == nil
}

// Postgres starts Postgres the way Compose does, with every service's roles
// and database, applies service's migrations as its owner, and returns the
// superuser's URL for service's database.
func (k *Kit) Postgres(service string) string {
	if k.err != nil {
		return ""
	}
	scripts, _ := filepath.Glob(filepath.Join(moduleRoot(), "deploy", "postgres", "*.sql")) // sorted
	c, err := postgres.Run(k.ctx, postgresImage,
		postgres.WithUsername("payments"),
		postgres.WithPassword("payments"),
		postgres.WithOrderedInitScripts(scripts...),
		postgres.BasicWaitStrategies(),
	)
	if !k.keep(c, err) {
		return ""
	}
	dsn, err := c.ConnectionString(k.ctx, "sslmode=disable")
	if err != nil {
		k.err = err
		return ""
	}
	u, _ := url.Parse(dsn)
	u.Path = "/" + service
	owner := *u
	owner.User = url.UserPassword(service+"_owner", service+"_owner")
	_, k.err = pg.Migrate(owner.String(), db.Migrations(service))
	return u.String()
}

// NATS starts a NATS server with JetStream, and returns its URL.
func (k *Kit) NATS() string {
	if k.err != nil {
		return ""
	}
	c, err := nats.Run(k.ctx, natsImage)
	if !k.keep(c, err) {
		return ""
	}
	url, err := c.ConnectionString(k.ctx)
	k.err = err
	return url
}

// Redis starts Redis, and returns its URL.
func (k *Kit) Redis() string {
	if k.err != nil {
		return ""
	}
	c, err := redis.Run(k.ctx, redisImage)
	if !k.keep(c, err) {
		return ""
	}
	url, err := c.ConnectionString(k.ctx)
	k.err = err
	return url
}

// moduleRoot finds the directory with go.mod. A test runs in its package's
// directory, somewhere below it.
func moduleRoot() string {
	dir, _ := os.Getwd()
	for dir != filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return dir
}
