//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/LerianStudio/lib-observability/v2/log"
	"github.com/bxcodec/dbresolver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// connectionMaxOpenMetric is one of the pool gauges sqlobs registers. Asserted
// by name so the test fails loudly if the upstream namespace ever drifts. This
// one carries exactly one data point per pool (unlike db.sql.connection.open,
// which is further split by an inuse/idle status label).
const connectionMaxOpenMetric = "db.sql.connection.max_open"

// poolRoleAttr is the low-cardinality primary/replica label sqlobs attaches.
const poolRoleAttr = "db.sql.pool.role"

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

// poolRoles returns the pool-role label of every connection-gauge data point
// currently collected.
func poolRoles(t *testing.T, reader *sdkmetric.ManualReader) []string {
	t.Helper()

	rm := &metricdata.ResourceMetrics{}
	require.NoError(t, reader.Collect(context.Background(), rm))

	var roles []string

	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != connectionMaxOpenMetric {
				continue
			}

			gauge, ok := m.Data.(metricdata.Gauge[int64])
			require.True(t, ok, "expected int64 gauge for %s, got %T", m.Name, m.Data)

			for _, dp := range gauge.DataPoints {
				if v, found := dp.Attributes.Value(attribute.Key(poolRoleAttr)); found {
					roles = append(roles, v.AsString())
				}
			}
		}
	}

	return roles
}

// connectedClient builds a client over the patched dependencies and connects it.
func connectedClient(t *testing.T) *Client {
	t.Helper()

	withPatchedDependencies(t,
		func(_, _ string) (*sql.DB, error) { return testDB(t), nil },
		func(_, _ *sql.DB, _ log.Logger) (dbresolver.DB, error) { return &fakeResolver{}, nil },
		nil,
	)

	client, err := New(validConfig())
	require.NoError(t, err)
	require.NoError(t, client.Connect(context.Background()))

	return client
}

// TestConnectRegistersPoolMetricsForBothRoles verifies the default pool metrics
// are emitted for the primary AND the replica without the service wiring
// anything — the whole point of centralizing the helper here.
func TestConnectRegistersPoolMetricsForBothRoles(t *testing.T) {
	reader := withGlobalMeterProvider(t)

	client := connectedClient(t)
	t.Cleanup(func() { _ = client.Close() })

	roles := poolRoles(t, reader)

	assert.ElementsMatch(t, []string{"primary", "replica"}, roles,
		"both pools must report db.sql.connection.* tagged with their role")
}

// TestCloseUnregistersPoolMetrics verifies the gauges stop being collected once
// the pools they observe are gone.
func TestCloseUnregistersPoolMetrics(t *testing.T) {
	reader := withGlobalMeterProvider(t)

	client := connectedClient(t)
	require.NotEmpty(t, poolRoles(t, reader))

	require.NoError(t, client.Close())

	assert.Empty(t, poolRoles(t, reader),
		"pool gauges must be unregistered on Close")
}

// TestReconnectDoesNotAccumulatePoolMetrics verifies a pool swap releases the
// previous registrations: without that, every reconnect leaves gauge callbacks
// observing a dead pool and the series double.
func TestReconnectDoesNotAccumulatePoolMetrics(t *testing.T) {
	reader := withGlobalMeterProvider(t)

	client := connectedClient(t)
	t.Cleanup(func() { _ = client.Close() })

	require.Len(t, poolRoles(t, reader), 2)

	require.NoError(t, client.Connect(context.Background()))

	assert.Len(t, poolRoles(t, reader), 2,
		"reconnect must swap the registrations, not add to them")
}

// TestInstrumentPoolNeverBreaksTheCaller verifies the degradation contract: a
// handle is always returned, along with a callable cleanup.
func TestInstrumentPoolNeverBreaksTheCaller(t *testing.T) {
	client, err := New(validConfig())
	require.NoError(t, err)

	raw := testDB(t)

	db, cleanup := client.instrumentPool(context.Background(), raw, validConfig().PrimaryDSN, "primary")

	require.NotNil(t, db)
	require.NotNil(t, cleanup)
	assert.NoError(t, cleanup())
}
