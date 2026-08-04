//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package postgres

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/internal/testutil"
	"github.com/bxcodec/dbresolver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hookedDB pings with a configurable error and runs onPing while the manager
// holds no lock, which is the exact window in which another goroutine can evict
// and rebuild the pool under the same tenant key.
type hookedDB struct {
	pingableDB

	onPing func()
}

func (h *hookedDB) PingContext(_ context.Context) error {
	if h.onPing != nil {
		h.onPing()
	}

	return h.pingErr
}

// newConnPair returns a cached connection wrapping the given DB.
func newConnPair(db dbresolver.DB) *PostgresConnection {
	iface := db

	return &PostgresConnection{ConnectionDB: &iface}
}

// newIdentityTestManager builds a manager whose Tenant Manager client points at
// a server that always fails, proving the assertions below never depend on a
// config fetch.
func newIdentityTestManager(t *testing.T) *Manager {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	tmClient := mustNewTestClient(t, server.URL)
	t.Cleanup(func() { _ = tmClient.Close() })

	return NewManager(tmClient, "ledger",
		WithLogger(testutil.NewCapturingLogger()),
		WithConnectionsCheckInterval(0),
	)
}

// TestGetConnection_HealthyPingDoesNotReturnRebuiltPool covers the TOCTOU
// re-check after the out-of-lock ping: the entry under the tenant key may have
// been evicted AND rebuilt while the ping was in flight, so an existence-only
// check hands the caller a pool that is no longer the cached one.
func TestGetConnection_HealthyPingDoesNotReturnRebuiltPool(t *testing.T) {
	t.Parallel()

	const tenantID = "tenant-identity-healthy"

	manager := newIdentityTestManager(t)

	freshDB := &pingableDB{}
	freshConn := newConnPair(freshDB)

	staleDB := &hookedDB{}
	staleDB.onPing = func() {
		manager.mu.Lock()
		manager.connections[tenantID] = freshConn
		manager.mu.Unlock()
	}

	staleConn := newConnPair(staleDB)

	manager.mu.Lock()
	manager.connections[tenantID] = staleConn
	manager.mu.Unlock()

	got, err := manager.GetConnection(context.Background(), tenantID)
	require.NoError(t, err)
	assert.Same(t, freshConn, got,
		"GetConnection must return the pool currently under the tenant key, not the one captured before the ping")
}

// TestGetConnection_UnhealthyPingDoesNotCloseRebuiltPool covers the eviction
// path: closing by key alone tears down whatever pool happens to be cached,
// including a healthy one another goroutine installed while the ping failed.
func TestGetConnection_UnhealthyPingDoesNotCloseRebuiltPool(t *testing.T) {
	t.Parallel()

	const tenantID = "tenant-identity-unhealthy"

	manager := newIdentityTestManager(t)

	freshDB := &pingableDB{}
	freshConn := newConnPair(freshDB)

	staleDB := &hookedDB{}
	staleDB.pingErr = errors.New("connection reset by peer")
	staleDB.onPing = func() {
		manager.mu.Lock()
		manager.connections[tenantID] = freshConn
		manager.mu.Unlock()
	}

	staleConn := newConnPair(staleDB)

	manager.mu.Lock()
	manager.connections[tenantID] = staleConn
	manager.mu.Unlock()

	got, err := manager.GetConnection(context.Background(), tenantID)
	require.NoError(t, err)

	assert.False(t, freshDB.closed,
		"the concurrently installed healthy pool must not be closed by the stale pool's eviction")
	assert.True(t, staleDB.closed,
		"the unhealthy pool that failed the ping must still be closed")
	assert.Same(t, freshConn, got,
		"the caller must receive the healthy rebuilt pool")

	manager.mu.RLock()
	current := manager.connections[tenantID]
	manager.mu.RUnlock()

	assert.Same(t, freshConn, current,
		"the fresh cache entry must survive the stale pool's eviction")
}

// TestGetConnection_ConcurrentEvictAndGet is the race-detector companion to the
// deterministic cases above.
func TestGetConnection_ConcurrentEvictAndGet(t *testing.T) {
	t.Parallel()

	const (
		tenantID = "tenant-identity-race"
		rounds   = 50
	)

	manager := newIdentityTestManager(t)

	manager.mu.Lock()
	manager.connections[tenantID] = newConnPair(&pingableDB{})
	manager.mu.Unlock()

	var wg sync.WaitGroup

	wg.Add(3)

	go func() {
		defer wg.Done()

		for range rounds {
			_, _ = manager.GetConnection(context.Background(), tenantID)
		}
	}()

	go func() {
		defer wg.Done()

		for range rounds {
			_ = manager.CloseConnection(context.Background(), tenantID)
		}
	}()

	go func() {
		defer wg.Done()

		for range rounds {
			manager.mu.Lock()
			manager.connections[tenantID] = newConnPair(&pingableDB{})
			manager.mu.Unlock()
		}
	}()

	wg.Wait()
}
