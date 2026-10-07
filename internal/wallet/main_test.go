package wallet_test

import (
	"testing"

	"github.com/fiston-user/payments/internal/platform/testkit"
)

// postgresURL is this package's own Postgres, started by TestMain.
var postgresURL string

func TestMain(m *testing.M) {
	testkit.Main(m, func(k *testkit.Kit) {
		postgresURL = k.Postgres("wallet")
	})
}
