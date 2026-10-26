package jobs_test

import (
	"testing"

	"github.com/fiston-user/payments/internal/platform/testkit"
)

var postgresURL string

// Any database will do for the locks: the wallet's has the job_runs table too.
func TestMain(m *testing.M) {
	testkit.Main(m, func(k *testkit.Kit) {
		postgresURL = k.Postgres("wallet")
	})
}
