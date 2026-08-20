//go:build unit

package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// operationDurationMetric is the per-command duration histogram redisotel
// records from its process hook — the redis side of the platform's
// operation-duration series. Asserted by name so the test fails loudly if the
// upstream instrument ever drifts.
const operationDurationMetric = "db.client.connections.use_time"

// poolStatMetric is one of the ASYNCHRONOUS pool-stat instruments. Its
// registration is owned by the MeterProvider rather than by the client, so it is
// exactly the one that outlives a replaced client when a cleanup is skipped.
// This one carries exactly one data point per pool (unlike
// db.client.connections.usage, which is further split by an idle/used state
// label), so a data point maps one-to-one to a live registration.
const poolStatMetric = "db.client.connections.max"

// poolNameAttr is the label redisotel attaches to every pool-stat series,
// defaulting to the server address. It is what keeps a live client's series
// distinguishable from a dead one's.
const poolNameAttr = "pool.name"

// releaseTimeout bounds the wait for an unregistration. redisotel drops the
// pool-stat registrations from a watcher goroutine woken by the close channel,
// so the release is observable shortly AFTER the cleanup returns, never inside
// it.
const releaseTimeout = 2 * time.Second

// withGlobalMeterProvider installs a collectable MeterProvider as the OTel
// global for the duration of the test. The instrumentation deliberately reads
// the globals (a service installs them via Telemetry.ApplyGlobals), so this is
// what the production path actually sees.
//
// WARNING: mutates global state — tests using it must NOT call t.Parallel().
func withGlobalMeterProvider(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	original := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)

	t.Cleanup(func() {
		otel.SetMeterProvider(original)
		_ = mp.Shutdown(context.Background())
	})

	return reader
}

// collect gathers the current metrics, tolerating failure by returning nothing.
// It is deliberately free of require/assert: assert.Eventually evaluates its
// condition on another goroutine, where a t.FailNow would be illegal.
func collect(reader *sdkmetric.ManualReader) *metricdata.ResourceMetrics {
	rm := &metricdata.ResourceMetrics{}
	if err := reader.Collect(context.Background(), rm); err != nil {
		return &metricdata.ResourceMetrics{}
	}

	return rm
}

// hasMetric reports whether an instrument of that name currently collects.
func hasMetric(reader *sdkmetric.ManualReader, name string) bool {
	for _, sm := range collect(reader).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return true
			}
		}
	}

	return false
}

// poolNames returns the pool.name label of every pool-stat data point currently
// collected — i.e. the set of clients still being observed. Because the label
// defaults to the server address, a registration left behind by a replaced
// client shows up here as the OLD address.
func poolNames(reader *sdkmetric.ManualReader) []string {
	var names []string

	for _, sm := range collect(reader).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != poolStatMetric {
				continue
			}

			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}

			for _, dp := range sum.DataPoints {
				if v, found := dp.Attributes.Value(attribute.Key(poolNameAttr)); found {
					names = append(names, v.AsString())
				}
			}
		}
	}

	return names
}

// TestConnectEmitsRedisMetrics verifies a plain connected client reports both
// instrument families without the service wiring anything — the whole point of
// centralizing this in lib-commons. The connect-time PING alone is enough to
// populate the command-duration histogram, because instrumentation is attached
// before the client is verified.
func TestConnectEmitsRedisMetrics(t *testing.T) {
	reader := withGlobalMeterProvider(t)
	mr := miniredis.RunT(t)

	client, err := New(context.Background(), newStandaloneConfig(mr.Addr()))
	require.NoError(t, err)

	t.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			t.Errorf("cleanup: client close: %v", closeErr)
		}
	})

	assert.True(t, hasMetric(reader, operationDurationMetric),
		"a connected client must report %s", operationDurationMetric)
	assert.Equal(t, []string{mr.Addr()}, poolNames(reader),
		"the live client must own exactly one pool-stat series")
}

// TestCloseUnregistersPoolMetrics verifies the asynchronous pool-stat callbacks
// stop being collected once the client they observe is gone.
func TestCloseUnregistersPoolMetrics(t *testing.T) {
	reader := withGlobalMeterProvider(t)
	mr := miniredis.RunT(t)

	client, err := New(context.Background(), newStandaloneConfig(mr.Addr()))
	require.NoError(t, err)
	require.NotEmpty(t, poolNames(reader))

	require.NoError(t, client.Close())

	assert.Eventually(t, func() bool {
		return !hasMetric(reader, poolStatMetric)
	}, releaseTimeout, 10*time.Millisecond,
		"pool-stat instruments must be unregistered on Close")
}

// TestReconnectDoesNotAccumulatePoolMetrics pins the bug this wiring exists to
// avoid: the pool-stat callbacks are owned by the MeterProvider, not by the
// client, so a reconnect that does not release the previous registration leaves
// it observing a dead client forever — one more set on every reconnect.
//
// The reconnect deliberately targets a DIFFERENT server: pool.name defaults to
// the address, so a leaked registration is visible as the old address lingering
// beside the new one.
func TestReconnectDoesNotAccumulatePoolMetrics(t *testing.T) {
	reader := withGlobalMeterProvider(t)
	first := miniredis.RunT(t)

	client, err := New(context.Background(), newStandaloneConfig(first.Addr()))
	require.NoError(t, err)

	t.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			t.Errorf("cleanup: client close: %v", closeErr)
		}
	})

	require.Equal(t, []string{first.Addr()}, poolNames(reader))

	second := miniredis.RunT(t)

	client.mu.Lock()
	client.cfg.Topology.Standalone = &StandaloneTopology{Address: second.Addr()}
	client.mu.Unlock()

	require.NoError(t, client.Connect(context.Background()))

	assert.Eventually(t, func() bool {
		names := poolNames(reader)

		return len(names) == 1 && names[0] == second.Addr()
	}, releaseTimeout, 10*time.Millisecond,
		"reconnect must swap the registration, not add to it")
}

// TestFailedConnectLeavesNoRegistration covers the discard path: the new client
// is instrumented before it is verified, so a failing PING must release that
// registration too — otherwise a server that is merely down slowly fills the
// meter with callbacks for clients that were never used.
func TestFailedConnectLeavesNoRegistration(t *testing.T) {
	reader := withGlobalMeterProvider(t)
	mr := miniredis.RunT(t)
	addr := mr.Addr()

	client, err := New(context.Background(), newStandaloneConfig(addr))
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = client.Close()
	})

	// Take the server away so the reconnect builds a client, fails to verify it,
	// and throws it away.
	mr.Close()

	client.mu.Lock()
	err = client.reconnectLocked(context.Background())
	client.mu.Unlock()

	require.Error(t, err, "reconnect must fail once the server is gone")

	// The previous client survives a failed reconnect, so exactly its own series
	// must remain: no series was added for the discarded client.
	assert.Eventually(t, func() bool {
		names := poolNames(reader)

		return len(names) == 1 && names[0] == addr
	}, releaseTimeout, 10*time.Millisecond,
		"a discarded client must not leave a registration behind")
}

// TestInstrumentClientNeverBreaksTheCaller pins the degradation contract: even
// on the worst input the helper returns a callable cleanup and only warns, so a
// telemetry failure can never fail a connect.
func TestInstrumentClientNeverBreaksTheCaller(t *testing.T) {
	logger := &recordingLogger{}
	client := &Client{logger: logger}

	cleanup := client.instrumentClient(context.Background(), nil)

	require.NotNil(t, cleanup)
	assert.NoError(t, cleanup())
	assert.NotEmpty(t, logger.warningMessages(),
		"a degraded instrumentation must be reported at warn")
}
