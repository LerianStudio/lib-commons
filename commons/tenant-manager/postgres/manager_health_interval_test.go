//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"
	"github.com/bxcodec/dbresolver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const healthIntervalTenant = "tenant-health-interval"

// newHealthIntervalManager returns a manager holding one cached pool for
// healthIntervalTenant, with async settings revalidation switched off so the
// assertions observe health-check pings and nothing else.
func newHealthIntervalManager(t *testing.T, opts ...Option) (*Manager, *pingableDB) {
	t.Helper()

	base := []Option{
		WithLogger(testutil.NewMockLogger()),
		WithConnectionsCheckInterval(0),
	}

	manager := NewManager(nil, "ledger", append(base, opts...)...)
	t.Cleanup(func() { require.NoError(t, manager.Close(context.Background())) })

	db := &pingableDB{}

	var resolver dbresolver.DB = db

	manager.mu.Lock()
	manager.connections[healthIntervalTenant] = &PostgresConnection{ConnectionDB: &resolver}
	manager.mu.Unlock()

	return manager, db
}

// getCachedConnection resolves the cached pool n times, failing on any error.
func getCachedConnection(t *testing.T, manager *Manager, n int) {
	t.Helper()

	for range n {
		conn, err := manager.GetConnection(context.Background(), healthIntervalTenant)
		require.NoError(t, err)
		require.NotNil(t, conn)
	}
}

// TestManager_GetConnection_HealthCheckIsIntervalGated pins the cost of a cache
// hit: with the default interval, resolving the same tenant twice in a row must
// cost one health-check round trip, not one per call.
func TestManager_GetConnection_HealthCheckIsIntervalGated(t *testing.T) {
	t.Parallel()

	manager, db := newHealthIntervalManager(t)

	getCachedConnection(t, manager, 2)

	assert.Equal(t, int32(1), db.pingCount(),
		"a cache hit inside the health-check window must not ping the tenant database")
}

// TestManager_GetConnection_HealthCheckRunsAfterInterval proves the gate is a
// delay and not a mute: once the window has elapsed, the next resolution pings.
func TestManager_GetConnection_HealthCheckRunsAfterInterval(t *testing.T) {
	t.Parallel()

	manager, db := newHealthIntervalManager(t, WithHealthCheckInterval(time.Nanosecond))

	getCachedConnection(t, manager, 2)

	assert.Equal(t, int32(2), db.pingCount(),
		"a cache hit after the health-check window must ping the tenant database again")
}

// TestManager_GetConnection_HealthCheckDisabledPingsEveryCall covers the escape
// hatch: a non-positive interval restores a ping on every resolution.
func TestManager_GetConnection_HealthCheckDisabledPingsEveryCall(t *testing.T) {
	t.Parallel()

	for name, interval := range map[string]time.Duration{
		"zero":     0,
		"negative": -5 * time.Second,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			manager, db := newHealthIntervalManager(t, WithHealthCheckInterval(interval))

			getCachedConnection(t, manager, 3)

			assert.Equal(t, int32(3), db.pingCount(),
				"a non-positive health-check interval must ping on every cache hit")
		})
	}
}
