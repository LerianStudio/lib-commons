//go:build integration

package pacing_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v6/commons/net/http/pacing"
	libRedis "github.com/LerianStudio/lib-commons/v6/commons/redis"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	pacingIntegrationTimeout = 2 * time.Minute
	integrationPrefix        = "dataprev"
)

// redisContainer is one Redis container whose lifetime a test can end on
// purpose, which is the only way to observe how the pacer behaves when the
// backend goes away for real.
type redisContainer struct {
	endpoint  string
	terminate func()
}

func startRedis(t *testing.T) *redisContainer {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), pacingIntegrationTimeout)
	t.Cleanup(cancel)

	container, err := tcredis.Run(ctx, "redis:7.4-alpine",
		testcontainers.WithWaitStrategy(
			wait.ForLog("Ready to accept connections").
				WithStartupTimeout(pacingIntegrationTimeout),
		),
	)
	require.NoError(t, err)

	var once sync.Once

	terminate := func() {
		once.Do(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cleanupCancel()

			if terminateErr := container.Terminate(cleanupCtx); terminateErr != nil {
				t.Errorf("terminate Redis container: %v", terminateErr)
			}
		})
	}

	t.Cleanup(terminate)

	endpoint, err := container.Endpoint(ctx, "")
	require.NoError(t, err)

	return &redisContainer{endpoint: endpoint, terminate: terminate}
}

func newIntegrationClient(t *testing.T, endpoint string) *libRedis.Client {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), pacingIntegrationTimeout)
	t.Cleanup(cancel)

	client, err := libRedis.New(ctx, libRedis.Config{
		Topology: libRedis.Topology{
			Standalone: &libRedis.StandaloneTopology{Address: endpoint},
		},
	})
	require.NoError(t, err)
	require.NoError(t, client.Connect(ctx))

	t.Cleanup(func() {
		// Logged, not failed: a test that terminates its container on purpose
		// leaves nothing for the client to close cleanly.
		if closeErr := client.Close(); closeErr != nil {
			t.Logf("close Redis client: %v", closeErr)
		}
	})

	return client
}

func flushRedis(t *testing.T, client *libRedis.Client) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	redisClient, err := client.GetClient(ctx)
	require.NoError(t, err)
	require.NoError(t, redisClient.FlushDB(ctx).Err())
}

func integrationRate(r float64) pacing.RateProvider {
	return func(context.Context) (float64, error) { return r, nil }
}

func TestIntegration_RedisPacer(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TLS", "true")

	client := newIntegrationClient(t, startRedis(t).endpoint)

	t.Run("tenant identity spellings all pace on real Redis", func(t *testing.T) {
		flushRedis(t, client)

		p, err := pacing.NewPacer(client, integrationPrefix)
		require.NoError(t, err)

		for _, id := range []string{
			"default",
			"tenant-123-abc",
			"1f2e3d4c-5b6a-7980-9182-a3b4c5d6e7f8",
			"1f2e3d4c5b6a79809182a3b4c5d6e7f8",
		} {
			bucket, bucketErr := pacing.TenantBucket(id, integrationRate(1000))
			require.NoErrorf(t, bucketErr, "tenant %q", id)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			require.NoErrorf(t, p.Acquire(ctx, bucket), "tenant %q", id)
			cancel()
		}
	})

	t.Run("real Redis stores a plain microsecond integer", func(t *testing.T) {
		flushRedis(t, client)

		p, err := pacing.NewPacer(client, integrationPrefix)
		require.NoError(t, err)

		bucket, err := pacing.TenantBucket("default", integrationRate(100))
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		require.NoError(t, p.Acquire(ctx, bucket))

		buckets := snapshotBuckets(t, client)
		require.Len(t, buckets, 1)

		for key, value := range buckets {
			// Redis converts a Lua number argument with a shortest-round-trip
			// formatter whose chosen spelling may be exponent notation. The script
			// formats explicitly so the stored arrival time stays a readable
			// 16-digit microsecond epoch; this is the only place that can observe it,
			// because test doubles format integers losslessly either way.
			assert.Regexp(t, `^[0-9]{16}$`, value, "bucket %s must hold a plain microsecond epoch", key)
		}
	})

	t.Run("two independent pacers share one combined budget", func(t *testing.T) {
		flushRedis(t, client)

		const (
			ratePerSecond = 5.0
			window        = time.Second
			// GCRA with a burst of one admits the first call immediately and then
			// one per 200ms emission interval: at most floor(1000/200)+1 = 6.
			maxGrantsInWindow = 6
			minGrantsInWindow = 4
		)

		first, err := pacing.NewPacer(client, integrationPrefix, pacing.WithPollInterval(10*time.Millisecond))
		require.NoError(t, err)

		second, err := pacing.NewPacer(client, integrationPrefix, pacing.WithPollInterval(10*time.Millisecond))
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), window)
		defer cancel()

		var (
			granted atomic.Int64
			wg      sync.WaitGroup
		)

		for _, p := range []*pacing.Pacer{first, second} {
			for range 4 {
				wg.Add(1)

				go func(pacer *pacing.Pacer) {
					defer wg.Done()

					for ctx.Err() == nil {
						bucket, bucketErr := pacing.TenantBucket("default", integrationRate(ratePerSecond))
						if bucketErr != nil {
							return
						}

						if pacer.Acquire(ctx, bucket) == nil {
							granted.Add(1)
						}
					}
				}(p)
			}
		}

		wg.Wait()

		total := granted.Load()
		t.Logf("combined grants in %s at %.0f/s: %d", window, ratePerSecond, total)
		assert.LessOrEqual(t, total, int64(maxGrantsInWindow),
			"two pacers on one Redis must enforce ONE budget, not two")
		assert.GreaterOrEqual(t, total, int64(minGrantsInWindow),
			"the shared budget must still be spent")
	})

	t.Run("refused multi-bucket acquire mutates no bucket", func(t *testing.T) {
		flushRedis(t, client)

		p, err := pacing.NewPacer(client, integrationPrefix, pacing.WithPollInterval(10*time.Millisecond))
		require.NoError(t, err)

		// One call per two seconds, so the second acquire cannot be granted
		// inside the deadline below.
		tenant, err := pacing.TenantBucket("default", integrationRate(0.5))
		require.NoError(t, err)

		inst, err := pacing.InstitutionBucket("077", integrationRate(0.5))
		require.NoError(t, err)

		grantCtx, grantCancel := context.WithTimeout(context.Background(), 10*time.Second)
		require.NoError(t, p.Acquire(grantCtx, tenant, inst))
		grantCancel()

		before := snapshotBuckets(t, client)
		require.Len(t, before, 2)

		refuseCtx, refuseCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer refuseCancel()

		require.ErrorIs(t, p.Acquire(refuseCtx, tenant, inst), pacing.ErrWaitAborted)
		assert.Equal(t, before, snapshotBuckets(t, client),
			"a refused evaluation must leave every bucket untouched")
	})

	t.Run("cancellation during a real wait fails closed", func(t *testing.T) {
		flushRedis(t, client)

		p, err := pacing.NewPacer(client, integrationPrefix, pacing.WithPollInterval(10*time.Millisecond))
		require.NoError(t, err)

		tenant, err := pacing.TenantBucket("default", integrationRate(0.5))
		require.NoError(t, err)

		grantCtx, grantCancel := context.WithTimeout(context.Background(), 10*time.Second)
		require.NoError(t, p.Acquire(grantCtx, tenant))
		grantCancel()

		waitCtx, waitCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer waitCancel()

		err = p.Acquire(waitCtx, tenant)
		require.ErrorIs(t, err, pacing.ErrWaitAborted)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})

	t.Run("zero rate blocks on real Redis without reserving", func(t *testing.T) {
		flushRedis(t, client)

		p, err := pacing.NewPacer(client, integrationPrefix, pacing.WithPollInterval(10*time.Millisecond))
		require.NoError(t, err)

		tenant, err := pacing.TenantBucket("default", integrationRate(0))
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		require.ErrorIs(t, p.Acquire(ctx, tenant), pacing.ErrWaitAborted)
		assert.Empty(t, snapshotBuckets(t, client), "a paused rate must reserve nothing")
	})
}

// TestIntegration_RedisPacer_BackendLossFailsClosed removes Redis from under a
// working pacer. Closing the lib-commons client would not do: GetClient
// reconnects on demand, so a closed client over a live server keeps granting.
func TestIntegration_RedisPacer_BackendLossFailsClosed(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TLS", "true")

	container := startRedis(t)
	client := newIntegrationClient(t, container.endpoint)

	p, err := pacing.NewPacer(client, integrationPrefix)
	require.NoError(t, err)

	tenant, err := pacing.TenantBucket("default", integrationRate(1000))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), pacingIntegrationTimeout)
	defer cancel()

	require.NoError(t, p.Acquire(ctx, tenant), "the pacer must work while Redis is up")

	container.terminate()

	// Repeated so a single refusal followed by a silent recovery cannot pass.
	for attempt := range 3 {
		acquireCtx, acquireCancel := context.WithTimeout(context.Background(), 5*time.Second)

		err := p.Acquire(acquireCtx, tenant)
		acquireCancel()

		require.ErrorIsf(t, err, pacing.ErrBackendUnavailable,
			"acquire %d must refuse while Redis is unreachable", attempt+1)
	}
}

// snapshotBuckets returns every pacing bucket key with its value, excluding the
// clock watermark, which is not a permit.
func snapshotBuckets(t *testing.T, client *libRedis.Client) map[string]string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	redisClient, err := client.GetClient(ctx)
	require.NoError(t, err)

	keys, err := redisClient.Keys(ctx, "pacing:*").Result()
	require.NoError(t, err)

	out := make(map[string]string, len(keys))

	for _, k := range keys {
		if len(k) >= len(":clock") && k[len(k)-len(":clock"):] == ":clock" {
			continue
		}

		value, valueErr := redisClient.Get(ctx, k).Result()
		require.NoError(t, valueErr)

		out[k] = value
	}

	return out
}
