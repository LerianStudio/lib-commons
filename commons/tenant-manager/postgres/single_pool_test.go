//go:build unit

package postgres

import (
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"

	"github.com/bxcodec/dbresolver/v2"
	"github.com/stretchr/testify/assert"
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
	assert.Empty(t, m.freshReplicaConnStr(withoutReplica))

	withReplica := &core.TenantConfig{
		Databases: map[string]core.DatabaseConfig{
			"onboarding": {
				PostgreSQL:        &core.PostgreSQLConfig{Host: "primary-host", Port: 5432, Database: "testdb"},
				PostgreSQLReplica: &core.PostgreSQLConfig{Host: "replica-host", Port: 5433, Database: "testdb"},
			},
		},
	}
	assert.Contains(t, m.freshReplicaConnStr(withReplica), "replica-host")
}
