package risk_test

import (
	"testing"

	"github.com/fiston-user/payments/internal/platform/testkit"
)

var redisURL string

func TestMain(m *testing.M) {
	testkit.Main(m, func(k *testkit.Kit) {
		redisURL = k.Redis()
	})
}
