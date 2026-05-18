//go:build unit

package mongo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/logcompat"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -------------------------------------------------------------------
// ApplyConnectionSettings — no-op coverage
// -------------------------------------------------------------------

func TestApplyConnectionSettings_IsNoOp(t *testing.T) {
	t.Parallel()

	c := &client.Client{}
	m := NewManager(c, "ledger")

	assert.NotPanics(t, func() {
		m.ApplyConnectionSettings("tenant-1", &core.TenantConfig{})
		m.ApplyConnectionSettings("tenant-2", nil)
	})
}

// -------------------------------------------------------------------
// GetDatabase — ManagerClosed
// -------------------------------------------------------------------

func TestGetDatabase_ManagerClosed(t *testing.T) {
	t.Parallel()

	c := &client.Client{}
	m := NewManager(c, "ledger")
	require.NoError(t, m.Close(context.Background()))

	_, err := m.GetDatabase(context.Background(), "tenant-1", "testdb")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrManagerClosed)
}

// -------------------------------------------------------------------
// GetDatabase — nil client
// -------------------------------------------------------------------

func TestGetDatabase_NilClient(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, "ledger")

	_, err := m.GetDatabase(context.Background(), "tenant-1", "testdb")
	require.Error(t, err)
}

// -------------------------------------------------------------------
// swapMongoConnection — replaces connection
// -------------------------------------------------------------------

func TestSwapMongoConnection_UpdatesEntry(t *testing.T) {
	t.Parallel()

	c := &client.Client{}
	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()))

	oldConn := &MongoConnection{DB: nil}
	m.connections["tenant-swap"] = oldConn
	m.lastAccessed["tenant-swap"] = time.Now().Add(-1 * time.Hour)
	m.databaseNames["tenant-swap"] = "olddb"

	newConn := &MongoConnection{DB: nil}
	m.swapMongoConnection(context.Background(), "tenant-swap", newConn, "newdb")

	m.mu.RLock()
	stored := m.connections["tenant-swap"]
	dbName := m.databaseNames["tenant-swap"]
	m.mu.RUnlock()

	assert.Same(t, newConn, stored, "new connection should replace old one")
	assert.Equal(t, "newdb", dbName)
}

// -------------------------------------------------------------------
// canStoreMongoConnection
// -------------------------------------------------------------------

func TestCanStoreMongoConnection_ManagerClosed(t *testing.T) {
	t.Parallel()

	c := &client.Client{}
	m := NewManager(c, "ledger")
	require.NoError(t, m.Close(context.Background()))

	// Use a fake DB to avoid panic in disconnectMongo
	fakeDB, cleanup := startFakeMongoServer(t)
	defer cleanup()

	conn := &MongoConnection{DB: fakeDB}
	result := m.canStoreMongoConnection(context.Background(), "tenant-1", conn)
	assert.False(t, result)
}

func TestCanStoreMongoConnection_TenantEvicted(t *testing.T) {
	t.Parallel()

	c := &client.Client{}
	m := NewManager(c, "ledger")

	// Use a fake DB to avoid panic in disconnectMongo
	fakeDB, cleanup := startFakeMongoServer(t)
	defer cleanup()

	conn := &MongoConnection{DB: fakeDB}
	result := m.canStoreMongoConnection(context.Background(), "tenant-not-in-map", conn)
	assert.False(t, result)
}

func TestCanStoreMongoConnection_TenantExistsBoost(t *testing.T) {
	t.Parallel()

	c := &client.Client{}
	m := NewManager(c, "ledger")

	m.mu.Lock()
	m.connections["tenant-exists-boost"] = &MongoConnection{DB: nil}
	m.mu.Unlock()

	fakeDB, cleanup := startFakeMongoServer(t)
	defer cleanup()

	conn := &MongoConnection{DB: fakeDB}
	result := m.canStoreMongoConnection(context.Background(), "tenant-exists-boost", conn)
	assert.True(t, result)
}

// -------------------------------------------------------------------
// reuseHealthyConnection — called via tryReuseCachedConnection with healthy conn
// -------------------------------------------------------------------

func TestTryReuseCachedConnection_NilDB_ReturnsFalse(t *testing.T) {
	t.Parallel()

	c := &client.Client{}
	m := NewManager(c, "ledger")

	conn := &MongoConnection{DB: nil}
	db, ok := m.tryReuseCachedConnection(context.Background(), "tenant-nildb", conn)
	assert.False(t, ok)
	assert.Nil(t, db)
}

// -------------------------------------------------------------------
// cacheConnection — manager closed path
// -------------------------------------------------------------------

func TestCacheConnection_ManagerClosed(t *testing.T) {
	t.Parallel()

	c := &client.Client{}
	m := NewManager(c, "ledger")
	require.NoError(t, m.Close(context.Background()))

	conn := &MongoConnection{DB: nil}
	_, err := m.cacheConnection(context.Background(), "tenant-1", conn, "testdb", testutil.NewMockLogger())
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrManagerClosed)
}

// -------------------------------------------------------------------
// disconnectUnhealthyConnection — covered indirectly
// -------------------------------------------------------------------

func TestDisconnectUnhealthyConnection_WithNilLogger(t *testing.T) {
	t.Parallel()

	// Build a fake connection from startFakeMongoServer
	fakeDB, cleanupFake := startFakeMongoServer(t)
	defer cleanupFake()

	c := &client.Client{}
	m := NewManager(c, "ledger") // no logger

	conn := &MongoConnection{DB: fakeDB}
	assert.NotPanics(t, func() {
		m.disconnectUnhealthyConnection(context.Background(), "tenant-1", conn, nil)
	})
}

// -------------------------------------------------------------------
// Connect — nil receiver
// -------------------------------------------------------------------

func TestConnect_NilReceiver(t *testing.T) {
	t.Parallel()

	var conn *MongoConnection
	err := conn.Connect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

// -------------------------------------------------------------------
// snapshotCachedConnection — closed manager
// -------------------------------------------------------------------

func TestSnapshotCachedConnection_ClosedManager(t *testing.T) {
	t.Parallel()

	c := &client.Client{}
	m := NewManager(c, "ledger")
	require.NoError(t, m.Close(context.Background()))

	_, _, err := m.snapshotCachedConnection("tenant-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrManagerClosed)
}

func TestSnapshotCachedConnection_OpenManager_NoCachedEntry(t *testing.T) {
	t.Parallel()

	c := &client.Client{}
	m := NewManager(c, "ledger")

	conn, hasCached, err := m.snapshotCachedConnection("tenant-unknown")
	require.NoError(t, err)
	assert.False(t, hasCached)
	assert.Nil(t, conn)
}

func TestSnapshotCachedConnection_HasCachedEntry(t *testing.T) {
	t.Parallel()

	c := &client.Client{}
	m := NewManager(c, "ledger")

	cached := &MongoConnection{DB: nil}
	m.connections["tenant-has-cache"] = cached

	conn, hasCached, err := m.snapshotCachedConnection("tenant-has-cache")
	require.NoError(t, err)
	assert.True(t, hasCached)
	assert.Same(t, cached, conn)
}

// -------------------------------------------------------------------
// createConnection — nil client
// -------------------------------------------------------------------

func TestCreateConnection_NilClient(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, "ledger")

	_, err := m.createConnection(context.Background(), "tenant-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant manager client is required")
}

// -------------------------------------------------------------------
// getMongoConfigForTenant — via HTTP server that returns 500
// -------------------------------------------------------------------

func TestGetMongoConfigForTenant_ConfigFetchFails(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	tmClient, err := client.NewClient(server.URL, testutil.NewMockLogger(),
		client.WithAllowInsecureHTTP(),
		client.WithServiceAPIKey("test-key"),
	)
	require.NoError(t, err)

	m := NewManager(tmClient, "ledger", WithLogger(testutil.NewMockLogger()))

	_, err = m.createConnection(context.Background(), "tenant-err")
	require.Error(t, err)
}

// -------------------------------------------------------------------
// detectAndReconnectMongo — no cached URI
// -------------------------------------------------------------------

func TestDetectAndReconnectMongo_NilConfig(t *testing.T) {
	t.Parallel()

	c := &client.Client{}
	m := NewManager(c, "ledger")

	cfg := &core.TenantConfig{}
	assert.NotPanics(t, func() {
		m.detectAndReconnectMongo(context.Background(), "tenant-1", cfg)
	})
}

func TestDetectAndReconnectMongo_NoCachedURI(t *testing.T) {
	t.Parallel()

	c := &client.Client{}
	m := NewManager(c, "ledger", WithLogger(testutil.NewMockLogger()))

	cfg := &core.TenantConfig{
		Databases: map[string]core.DatabaseConfig{
			"ledger": {
				MongoDB: &core.MongoDBConfig{
					Host:     "mongo-host",
					Port:     27017,
					Database: "testdb",
				},
			},
		},
	}

	assert.NotPanics(t, func() {
		m.detectAndReconnectMongo(context.Background(), "tenant-nocache", cfg)
	})
}

// -------------------------------------------------------------------
// getMongoConfigForTenant — nil client guard (via createConnection)
// -------------------------------------------------------------------

func TestGetMongoConfigForTenant_NilClient(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, "ledger")
	_, err := m.createConnection(context.Background(), "tenant-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant manager client is required")
}

// -------------------------------------------------------------------
// revalidatePoolSettings — panic recovery guard
// -------------------------------------------------------------------

func TestRevalidatePoolSettings_PanicGuard(t *testing.T) {
	t.Parallel()

	capLogger := testutil.NewCapturingLogger()
	m := &Manager{
		logger:                   logcompat.New(capLogger),
		connections:              make(map[string]*MongoConnection),
		databaseNames:            make(map[string]string),
		lastAccessed:             make(map[string]time.Time),
		lastConnectionsCheck:     make(map[string]time.Time),
		connectionsCheckInterval: 1 * time.Millisecond,
	}

	assert.NotPanics(t, func() {
		m.revalidatePoolSettings("tenant-panic")
	})
}

// -------------------------------------------------------------------
// Stats — various states
// -------------------------------------------------------------------

func TestStats_ClosedManager(t *testing.T) {
	t.Parallel()

	c := &client.Client{}
	m := NewManager(c, "ledger")
	require.NoError(t, m.Close(context.Background()))

	stats := m.Stats()
	assert.True(t, stats.Closed)
}
