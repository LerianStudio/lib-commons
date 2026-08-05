//go:build integration

package idempotency_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	libConstants "github.com/LerianStudio/lib-commons/v6/commons/constants"
	"github.com/LerianStudio/lib-commons/v6/commons/net/http/idempotency"
	"github.com/LerianStudio/lib-commons/v6/commons/net/http/idempotency/idempotencytest"
	libRedis "github.com/LerianStudio/lib-commons/v6/commons/redis"
	tmcore "github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
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

	t.Run("legacy bridge uses the v6.2 Redis layout and replays it", func(t *testing.T) {
		redisClient, clientErr := client.GetClient(ctx)
		require.NoError(t, clientErr)
		require.NoError(t, redisClient.FlushDB(ctx).Err())

		const (
			canonicalTenant = "550e8400e29b41d4a716446655440000"
			originalTenant  = "550e8400-e29b-41d4-a716-446655440000"
			requestKey      = "integration-bridge"
		)

		var handlerCalls atomic.Int32
		middleware := idempotency.New(client, idempotency.WithRedisLegacyBridge())
		app := fiber.New()
		app.Use(func(c fiber.Ctx) error {
			requestCtx := tmcore.ContextWithTenantID(c.Context(), canonicalTenant)
			requestCtx = tmcore.ContextWithOriginalTenantID(requestCtx, originalTenant)
			c.SetContext(requestCtx)

			return c.Next()
		})
		app.Use(middleware.Check())
		app.Post("/bridge", func(c fiber.Ctx) error {
			handlerCalls.Add(1)

			return c.Status(http.StatusAccepted).SendString("created")
		})

		send := func() *http.Response {
			request := httptest.NewRequest(http.MethodPost, "/bridge", strings.NewReader(`{"amount":10}`))
			request.Header.Set(libConstants.IdempotencyKey, requestKey)
			response, requestErr := app.Test(request)
			require.NoError(t, requestErr)

			return response
		}

		first := send()
		firstBody, readErr := io.ReadAll(first.Body)
		require.NoError(t, readErr)
		require.NoError(t, first.Body.Close())
		assert.Equal(t, http.StatusAccepted, first.StatusCode)
		assert.Equal(t, "created", string(firstBody))

		redisKey := "idempotency:" + originalTenant + ":" + requestKey
		marker, getErr := redisClient.Get(ctx, redisKey).Result()
		require.NoError(t, getErr)
		assert.True(t, strings.HasPrefix(marker, "complete:"))
		assert.False(t, strings.HasPrefix(marker, "{"), "bridge marker must remain readable by v6.2 pods")
		assert.NoError(t, redisClient.Get(ctx, redisKey+":response").Err())
		assert.Zero(t, redisClient.Exists(ctx, redisKey+":bridge-owner").Val())
		canonicalKey := "idempotency:" + canonicalTenant + ":" + requestKey
		canonicalJSON, getErr := redisClient.Get(ctx, canonicalKey).Bytes()
		require.NoError(t, getErr)
		var canonicalRecord struct {
			State       string `json:"state"`
			Fingerprint string `json:"fingerprint"`
			Owner       string `json:"owner"`
			Response    []byte `json:"response"`
		}
		require.NoError(t, json.Unmarshal(canonicalJSON, &canonicalRecord))
		assert.Equal(t, "complete", canonicalRecord.State)
		assert.NotEmpty(t, canonicalRecord.Fingerprint)
		assert.NotEmpty(t, canonicalRecord.Owner)
		assert.NotEmpty(t, canonicalRecord.Response)

		second := send()
		secondBody, readErr := io.ReadAll(second.Body)
		require.NoError(t, readErr)
		require.NoError(t, second.Body.Close())
		assert.Equal(t, http.StatusAccepted, second.StatusCode)
		assert.Equal(t, "created", string(secondBody))
		assert.Equal(t, "true", second.Header.Get(libConstants.IdempotencyReplayed))
		assert.Equal(t, int32(1), handlerCalls.Load())

		currentApp := fiber.New()
		currentApp.Use(func(c fiber.Ctx) error {
			requestCtx := tmcore.ContextWithTenantID(c.Context(), canonicalTenant)
			requestCtx = tmcore.ContextWithOriginalTenantID(requestCtx, originalTenant)
			c.SetContext(requestCtx)

			return c.Next()
		})
		currentApp.Use(idempotency.New(client).Check())
		currentApp.Post("/bridge", func(c fiber.Ctx) error {
			handlerCalls.Add(1)

			return c.Status(http.StatusCreated).SendString("unexpected")
		})
		currentRequest := httptest.NewRequest(http.MethodPost, "/bridge", strings.NewReader(`{"amount":10}`))
		currentRequest.Header.Set(libConstants.IdempotencyKey, requestKey)
		currentResponse, requestErr := currentApp.Test(currentRequest)
		require.NoError(t, requestErr)
		currentBody, readErr := io.ReadAll(currentResponse.Body)
		require.NoError(t, readErr)
		require.NoError(t, currentResponse.Body.Close())
		assert.Equal(t, http.StatusAccepted, currentResponse.StatusCode)
		assert.Equal(t, "created", string(currentBody))
		assert.Equal(t, "true", currentResponse.Header.Get(libConstants.IdempotencyReplayed))
		assert.Equal(t, int32(1), handlerCalls.Load())
	})
}
