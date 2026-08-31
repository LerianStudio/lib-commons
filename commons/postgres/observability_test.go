//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/LerianStudio/lib-commons/v6/commons/obs"

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

	return connectedClientNamed(t, "")
}

// connectedClientNamed is connectedClient with an explicit Config.DatabaseName.
func connectedClientNamed(t *testing.T, databaseName string) *Client {
	t.Helper()

	withPatchedDependencies(t,
		func(_, _ string) (*sql.DB, error) { return testDB(t), nil },
		func(_, _ *sql.DB, _ obs.Logger) (dbresolver.DB, error) { return &fakeResolver{}, nil },
		nil,
	)

	cfg := validConfig()
	cfg.DatabaseName = databaseName

	client, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, client.Connect(context.Background()))

	return client
}

// poolSeries returns a "role/namespace" identity for every connection-gauge data
// point, so a test can assert that distinct databases stay in distinct series.
func poolSeries(t *testing.T, reader *sdkmetric.ManualReader) []string {
	t.Helper()

	rm := &metricdata.ResourceMetrics{}
	require.NoError(t, reader.Collect(context.Background(), rm))

	var series []string

	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != connectionMaxOpenMetric {
				continue
			}

			gauge, ok := m.Data.(metricdata.Gauge[int64])
			require.True(t, ok, "expected int64 gauge for %s, got %T", m.Name, m.Data)

			for _, dp := range gauge.DataPoints {
				role, _ := dp.Attributes.Value(attribute.Key(poolRoleAttr))
				ns, _ := dp.Attributes.Value(attribute.Key(dbNamespaceAttrKey))
				series = append(series, role.AsString()+"/"+ns.AsString())
			}
		}
	}

	return series
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

// TestDatabaseNameSeparatesPoolSeries pins the failure found on a real midaz
// ledger: two clients against different databases share the {system, role} label
// set, and because the pool gauges are asynchronous instruments one silently
// overwrites the other. Config.DatabaseName keeps them apart.
func TestDatabaseNameSeparatesPoolSeries(t *testing.T) {
	reader := withGlobalMeterProvider(t)

	onboarding := connectedClientNamed(t, "onboarding")
	t.Cleanup(func() { _ = onboarding.Close() })

	transaction := connectedClientNamed(t, "transaction")
	t.Cleanup(func() { _ = transaction.Close() })

	assert.ElementsMatch(t,
		[]string{
			"primary/onboarding", "replica/onboarding",
			"primary/transaction", "replica/transaction",
		},
		poolSeries(t, reader),
		"each database must own its pool series")
}

// TestNamespaceFallsBackToDSN verifies a caller that sets no DatabaseName still
// gets the label, derived from the DSN of each pool.
func TestNamespaceFallsBackToDSN(t *testing.T) {
	reader := withGlobalMeterProvider(t)

	client := connectedClient(t)
	t.Cleanup(func() { _ = client.Close() })

	// validConfig points both pools at the "postgres" database.
	assert.ElementsMatch(t, []string{"primary/postgres", "replica/postgres"},
		poolSeries(t, reader), "db.namespace must be derived from the DSN")
}

// TestDSNDatabaseName covers both DSN forms the library accepts, plus the
// degenerate inputs that must simply yield no label.
func TestDSNDatabaseName(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want string
	}{
		{"url form", "postgres://user:pw@host:5432/onboarding?sslmode=disable", "onboarding"},
		{"url form postgresql scheme", "postgresql://user:pw@host:5432/transaction", "transaction"},
		{"url form without database", "postgres://user:pw@host:5432", ""},
		{"key-value form", "host=h user=u password=p dbname=onboarding port=5432 sslmode=disable", "onboarding"},
		{"key-value form quoted", "host=h dbname='transaction' port=5432", "transaction"},
		{"key-value form without dbname", "host=h user=u port=5432", ""},
		{"empty", "", ""},
		{"garbage", "%%not-a-dsn%%", ""},
		// libpq quoting rules: a quoted value keeps its spaces, and a backslash
		// escapes the next character. Splitting on whitespace truncates these.
		{"quoted with spaces", "host=h dbname='tenant alpha' port=5432", "tenant alpha"},
		{"quoted with escaped quote", `host=h dbname='tenant\'s db' port=5432`, "tenant's db"},
		{"quoted with escaped backslash", `host=h dbname='tenant\\db' port=5432`, `tenant\db`},
		// pgx recognizes only single quotes: the double quotes are part of the
		// value, and the label must name what pgx actually connects to.
		{"double quoted kept verbatim like pgx", `host=h dbname="ledger" port=5432`, `"ledger"`},
		// pgx unescapes only \\ and \' — any other backslash stays.
		{"unquoted backslash kept like pgx", `host=h dbname=tenant\q port=5432`, `tenant\q`},
		// Case-sensitive keywords: pgx does not read DBNAME.
		{"uppercase DBNAME ignored like pgx", "host=h DBNAME=ledger port=5432", ""},
		{"spaces around equals", "host = h dbname = ledger port=5432", "ledger"},
		// pgx errors on an unterminated quote — no connection ever exists, so
		// there is nothing to label.
		{"unterminated quote is malformed", "host=h dbname='ledger", ""},
		{"stray token is malformed", "host dbname=ledger", ""},
		{"missing equals entirely", "hostonly", ""},
		{"empty key is malformed", "=ledger dbname=ledger", ""},
		{"trailing backslash is malformed", `host=h dbname=ledger\`, ""},
		// pgx keeps the LAST duplicate setting; the label must name the
		// database pgx actually connects to.
		{"repeated dbname last wins", "host=h dbname=old dbname=new port=5432", "new"},
		{"form feed separator", "host=h\fdbname=ledger", "ledger"},
		// Guardrail: the label may only ever come from the real dbname keyword,
		// never from text carried inside another value.
		{"dbname inside a quoted value", "host=h password='p dbname=leaked' dbname=ledger", "ledger"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, dsnDatabaseName(tc.dsn))
		})
	}
}

// TestDSNDatabaseNameLeaksNoCredential pins the guardrail: whatever the DSN
// carries, only the database name may come out.
func TestDSNDatabaseNameLeaksNoCredential(t *testing.T) {
	got := dsnDatabaseName("postgres://admin:sup3rs3cret@host:5432/ledger?sslmode=require")

	assert.Equal(t, "ledger", got)
	assert.NotContains(t, got, "sup3rs3cret")
	assert.NotContains(t, got, "admin")
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

// TestInstrumentPoolDegradesToTheRawPool covers the failing half of that
// contract. sqlobs refuses to swap a pool it cannot re-open by DSN (an empty DSN
// returns ErrDSNRequired), and the caller must come out with its own working
// handle and a callable no-op cleanup — telemetry is never worth connectivity.
func TestInstrumentPoolDegradesToTheRawPool(t *testing.T) {
	client, err := New(validConfig())
	require.NoError(t, err)

	raw := testDB(t)

	db, cleanup := client.instrumentPool(context.Background(), raw, "", "primary")

	assert.Same(t, raw, db, "a degraded instrumentation must hand back the caller's own pool")
	require.NotNil(t, cleanup)
	assert.NoError(t, cleanup())
}
