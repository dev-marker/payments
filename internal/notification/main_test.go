package notification_test

import (
	"testing"

	"github.com/fiston-user/payments/internal/platform/testkit"
)

var postgresURL, natsURL string

func TestMain(m *testing.M) {
	testkit.Main(m, func(k *testkit.Kit) {
		postgresURL = k.Postgres("notification")
		natsURL = k.NATS()
	})
}
