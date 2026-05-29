//go:build unit

package postgres

import (
	"time"

	"github.com/bxcodec/dbresolver/v2"
)

// InjectCachedConnectionForTest pre-populates the manager's connection cache
// with the supplied dbresolver.DB, so subsequent GetConnection(tenantID) calls
// return the cached entry instead of attempting an HTTP / SQL round-trip.
//
// Only available under the "unit" build tag; never compiled into production
// binaries. Use exclusively from unit tests that need to exercise downstream
// code paths without spinning up a real Postgres backend.
func InjectCachedConnectionForTest(m *Manager, tenantID string, db dbresolver.DB) {
	if m == nil || tenantID == "" || db == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.connections == nil {
		m.connections = make(map[string]*PostgresConnection)
	}

	if m.lastAccessed == nil {
		m.lastAccessed = make(map[string]time.Time)
	}

	if m.lastConnectionsCheck == nil {
		m.lastConnectionsCheck = make(map[string]time.Time)
	}

	resolverDB := db
	m.connections[tenantID] = &PostgresConnection{
		ConnectionDB: &resolverDB,
	}
	m.lastAccessed[tenantID] = time.Now()
	// Future-dated to suppress async revalidation goroutines during tests.
	m.lastConnectionsCheck[tenantID] = time.Now().Add(time.Hour)
}
