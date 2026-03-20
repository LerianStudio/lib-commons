package consumer

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/alicebob/miniredis/v2/server.(*Server).servePeer"),
		goleak.IgnoreTopFunction("github.com/alicebob/miniredis/v2.(*Miniredis).handleClient"),
		goleak.IgnoreTopFunction("github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/cache.(*InMemoryCache).cleanupLoop"),
		goleak.IgnoreTopFunction("github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/consumer.(*MultiTenantConsumer).superviseTenantQueues"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
	)
}
