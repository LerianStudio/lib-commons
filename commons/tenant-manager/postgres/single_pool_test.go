//go:build unit

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"

	"github.com/bxcodec/dbresolver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	singlePoolPrimaryDSN = "postgres://user:pass@primary-host:5432/testdb?sslmode=disable"
	singlePoolReplicaDSN = "postgres://user:pass@replica-host:5433/testdb?sslmode=disable"
)

// TestHasPostgresConfigChanged_ReplicaSemantics covers the reconnect detector
// with the replica DSN in every shape it can take: absent, a legacy primary
// copy (connections cached by an older build), and a real replica.
func TestHasPostgresConfigChanged_ReplicaSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		cachedReplica string
		freshPrimary  string
		freshReplica  string
		want          bool
	}{
		{name: "no replica before and after", cachedReplica: "", freshPrimary: singlePoolPrimaryDSN, freshReplica: "", want: false},
		{name: "legacy primary copy vs no replica", cachedReplica: singlePoolPrimaryDSN, freshPrimary: singlePoolPrimaryDSN, freshReplica: "", want: false},
		{name: "replica appears", cachedReplica: "", freshPrimary: singlePoolPrimaryDSN, freshReplica: singlePoolReplicaDSN, want: true},
		{name: "replica removed", cachedReplica: singlePoolReplicaDSN, freshPrimary: singlePoolPrimaryDSN, freshReplica: "", want: true},
		{name: "same replica", cachedReplica: singlePoolReplicaDSN, freshPrimary: singlePoolPrimaryDSN, freshReplica: singlePoolReplicaDSN, want: false},
		{name: "primary changes without replica", cachedReplica: "", freshPrimary: "postgres://user:pass@other-host:5432/testdb?sslmode=disable", freshReplica: "", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := mustNewTestClient(t, "http://localhost:8080")
			m := NewManager(c, "ledger")

			var db dbresolver.DB = &pingableDB{}

			m.connections["tenant-1"] = &PostgresConnection{
				ConnectionStringPrimary: singlePoolPrimaryDSN,
				ConnectionStringReplica: tt.cachedReplica,
				ConnectionDB:            &db,
			}
			m.lastAccessed["tenant-1"] = time.Now()

			assert.Equal(t, tt.want, m.hasPostgresConfigChanged("tenant-1", tt.freshPrimary, tt.freshReplica))
		})
	}
}

// TestFreshReplicaConnStr_NoReplicaConfig_IsEmpty pins the fresh-side
// computation used by the reconnect detector: with no replica in the tenant
// config the fresh replica DSN is empty, matching what resolveReplicaConnection
// stores at connection time.
func TestFreshReplicaConnStr_NoReplicaConfig_IsEmpty(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger", WithModule("onboarding"))

	withoutReplica := &core.TenantConfig{
		Databases: map[string]core.DatabaseConfig{
			"onboarding": {PostgreSQL: &core.PostgreSQLConfig{Host: "primary-host", Port: 5432, Database: "testdb"}},
		},
	}
	freshReplica, err := m.freshReplicaConnStr(withoutReplica)
	require.NoError(t, err)
	assert.Empty(t, freshReplica)

	withReplica := &core.TenantConfig{
		Databases: map[string]core.DatabaseConfig{
			"onboarding": {
				PostgreSQL:        &core.PostgreSQLConfig{Host: "primary-host", Port: 5432, Database: "testdb"},
				PostgreSQLReplica: &core.PostgreSQLConfig{Host: "replica-host", Port: 5433, Database: "testdb"},
			},
		},
	}
	freshReplica, err = m.freshReplicaConnStr(withReplica)
	require.NoError(t, err)
	assert.Contains(t, freshReplica, "replica-host")
}

// malformedReplicaTenantConfig returns a tenant config whose primary builds
// the given DSN and whose replica is unbuildable: sslmode=disable together
// with a root certificate is the contradiction BuildConnectionString rejects.
func malformedReplicaTenantConfig() *core.TenantConfig {
	return &core.TenantConfig{
		Databases: map[string]core.DatabaseConfig{
			"onboarding": {
				PostgreSQL: &core.PostgreSQLConfig{
					Host: "primary-host", Port: 5432, Database: "testdb",
					Username: "user", Password: "pass", SSLMode: "disable",
				},
				PostgreSQLReplica: &core.PostgreSQLConfig{
					Host: "replica-host", Port: 5433, Database: "testdb",
					Username: "user", Password: "pass", SSLMode: "disable", SSLRootCert: "/certs/root.pem",
				},
			},
		},
	}
}

// TestFreshReplicaConnStr_MalformedReplica_ReturnsError pins that an
// unbuildable replica is reported as an error, not collapsed into "" -- the
// spelling of "no replica" -- which would make it invisible to change
// detection.
func TestFreshReplicaConnStr_MalformedReplica_ReturnsError(t *testing.T) {
	t.Parallel()

	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger", WithModule("onboarding"))

	freshReplica, err := m.freshReplicaConnStr(malformedReplicaTenantConfig())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sslrootcert")
	assert.Empty(t, freshReplica)
}

// TestDetectAndReconnectPostgres_MalformedReplicaOnSinglePoolTenant_WarnsAndKeepsConnection
// is the regression for a cached single-pool tenant whose config later gains a
// MALFORMED replica. Before the fix the build error was swallowed into "", the
// detector saw "no replica before, no replica after", and the operator never
// learned their replica config was being ignored. Now the detector warns at
// WARN level naming the tenant and keeps the current connection: the same
// treatment an unbuildable primary DSN already receives on this path.
func TestDetectAndReconnectPostgres_MalformedReplicaOnSinglePoolTenant_WarnsAndKeepsConnection(t *testing.T) {
	t.Parallel()

	logger := testutil.NewLevelCapturingLogger()
	c := mustNewTestClient(t, "http://localhost:8080")
	m := NewManager(c, "ledger", WithModule("onboarding"), WithLogger(logger))

	var db dbresolver.DB = &pingableDB{}

	cached := &PostgresConnection{
		ConnectionStringPrimary: singlePoolPrimaryDSN,
		ConnectionStringReplica: "",
		ConnectionDB:            &db,
	}
	m.connections["tenant-1"] = cached
	m.lastAccessed["tenant-1"] = time.Now()

	reconnected := m.detectAndReconnectPostgres(context.Background(), "tenant-1", malformedReplicaTenantConfig())

	assert.False(t, reconnected, "an unbuildable replica must not be reported as a reconnection attempt")
	assert.Same(t, cached, m.connections["tenant-1"], "the current connection must be kept")
	assert.True(t, logger.ContainsAtLevel(obs.LevelWarn, "invalid replica connection string", "tenant-1"),
		"operator must be told the replica config is being ignored; got %v", logger.Entries())

	// Positive control: with the replica config removed the same detector on the
	// same tenant stays silent, proving the warning above is tied to the
	// malformed replica and not to the fixture.
	logger2 := testutil.NewLevelCapturingLogger()
	m2 := NewManager(c, "ledger", WithModule("onboarding"), WithLogger(logger2))
	m2.connections["tenant-1"] = cached
	m2.lastAccessed["tenant-1"] = time.Now()

	withoutReplica := malformedReplicaTenantConfig()
	dbCfg := withoutReplica.Databases["onboarding"]
	dbCfg.PostgreSQLReplica = nil
	withoutReplica.Databases["onboarding"] = dbCfg

	assert.False(t, m2.detectAndReconnectPostgres(context.Background(), "tenant-1", withoutReplica))
	assert.False(t, logger2.ContainsAtLevel(obs.LevelWarn, "invalid replica connection string"),
		"no warning expected without a replica; got %v", logger2.Entries())
}
