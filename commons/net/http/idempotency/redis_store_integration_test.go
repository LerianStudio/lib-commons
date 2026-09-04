//go:build integration

package idempotency_test

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/net/http/idempotency"
	"github.com/LerianStudio/lib-commons/v7/commons/net/http/idempotency/idempotencytest"
	libRedis "github.com/LerianStudio/lib-commons/v7/commons/redis"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

const redisStoreIntegrationTimeout = 2 * time.Minute

func TestIntegration_RedisStore_Contract(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TLS", "true")

	ctx, cancel := context.WithTimeout(context.Background(), redisStoreIntegrationTimeout)
	t.Cleanup(cancel)

	container, err := tcredis.Run(ctx, "redis:7.4-alpine",
		testcontainers.WithWaitStrategy(
			wait.ForLog("Ready to accept connections").
				WithStartupTimeout(redisStoreIntegrationTimeout),
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()

		if terminateErr := container.Terminate(cleanupCtx); terminateErr != nil {
			t.Errorf("terminate Redis container: %v", terminateErr)
		}
	})

	endpoint, err := container.Endpoint(ctx, "")
	require.NoError(t, err)

	client, err := libRedis.New(ctx, libRedis.Config{
		Topology: libRedis.Topology{
			Standalone: &libRedis.StandaloneTopology{Address: endpoint},
		},
	})
	require.NoError(t, err)
	require.NoError(t, client.Connect(ctx))
	t.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			t.Errorf("close Redis client: %v", closeErr)
		}
	})

	idempotencytest.Run(t, func(t *testing.T) idempotency.Store {
		t.Helper()

		redisClient, clientErr := client.GetClient(ctx)
		require.NoError(t, clientErr)
		require.NoError(t, redisClient.FlushDB(ctx).Err())

		return idempotency.NewRedisStore(client)
	})
}
