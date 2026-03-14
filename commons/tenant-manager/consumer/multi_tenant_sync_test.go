package consumer

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultiTenantConsumer_SyncTenants_EagerModeStartsNewTenant(t *testing.T) {
	t.Parallel()

	mr, redisClient := setupMiniredis(t)
	mr.SAdd(testActiveTenantsKey, "tenant-a")

	consumer := NewMultiTenantConsumer(dummyRabbitMQManager(), redisClient, MultiTenantConfig{
		SyncInterval:    30 * time.Second,
		WorkersPerQueue: 1,
		PrefetchCount:   10,
		Service:         testServiceName,
		EagerStart:      true,
	}, testutil.NewMockLogger())
	defer func() { require.NoError(t, consumer.Close()) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	consumer.discoverTenants(ctx)
	mr.SAdd(testActiveTenantsKey, "tenant-b")

	err := consumer.syncTenants(ctx)
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		consumer.mu.RLock()
		defer consumer.mu.RUnlock()

		_, active := consumer.tenants["tenant-b"]
		return consumer.knownTenants["tenant-b"] && active
	}, time.Second, 10*time.Millisecond)
}
