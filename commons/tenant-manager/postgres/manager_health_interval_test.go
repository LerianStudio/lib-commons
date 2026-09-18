//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package postgres

import (
	"context"
	"errors"
	"sync/atomic"
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

	db := &pingableDB{}
	manager := newHealthIntervalManagerWithDB(t, db, opts...)

	return manager, db
}

// newHealthIntervalManagerWithDB caches the supplied pool under
// healthIntervalTenant and returns the manager owning it.
func newHealthIntervalManagerWithDB(t *testing.T, db dbresolver.DB, opts ...Option) *Manager {
	t.Helper()

	base := []Option{
		WithLogger(testutil.NewMockLogger()),
		WithConnectionsCheckInterval(0),
	}

	manager := NewManager(nil, "ledger", append(base, opts...)...)
	t.Cleanup(func() { require.NoError(t, manager.Close(context.Background())) })

	resolver := db

	manager.mu.Lock()
	manager.connections[healthIntervalTenant] = &PostgresConnection{ConnectionDB: &resolver}
	manager.mu.Unlock()

	return manager
}

// blockingDB is a pingableDB whose ping parks until release is closed, so a test can
// hold a health check in flight while a second caller resolves the same tenant.
type blockingDB struct {
	pingableDB

	started chan struct{}
	release chan struct{}
}

func newBlockingDB() *blockingDB {
	return &blockingDB{
		started: make(chan struct{}, 4),
		release: make(chan struct{}),
	}
}

func (b *blockingDB) PingContext(_ context.Context) error {
	atomic.AddInt32(&b.pings, 1)

	b.started <- struct{}{}
	<-b.release

	return b.pingErr
}

// connResult is what a concurrent GetConnection call handed back.
type connResult struct {
	conn *PostgresConnection
	err  error
}

// resolveAsync resolves healthIntervalTenant on its own goroutine.
func resolveAsync(manager *Manager) <-chan connResult {
	out := make(chan connResult, 1)

	go func() {
		conn, err := manager.GetConnection(context.Background(), healthIntervalTenant)
		out <- connResult{conn: conn, err: err}
	}()

	return out
}

// TestManager_GetConnection_ConcurrentCallerWaitsForFailedCheck is the pool-pulled-
// from-under-you case: while one caller's health check is in flight, a second caller
// for the same tenant must not be handed that pool, because the check may still
// condemn it. Before the interval gate every caller ran its own check and saw the
// failure itself; a caller that skips the check must therefore wait for the verdict.
func TestManager_GetConnection_ConcurrentCallerWaitsForFailedCheck(t *testing.T) {
	t.Parallel()

	db := newBlockingDB()
	manager := newHealthIntervalManagerWithDB(t, db)

	first := resolveAsync(manager)

	<-db.started // the health check is in flight

	second := resolveAsync(manager)

	select {
	case got := <-second:
		t.Fatalf("second caller returned before the in-flight health check resolved: conn=%p err=%v", got.conn, got.err)
	case <-time.After(100 * time.Millisecond):
	}

	db.pingErr = errors.New("connection reset by peer")
	close(db.release)

	firstResult := <-first
	secondResult := <-second

	require.Error(t, firstResult.err, "the caller that ran the failed check has no Tenant Manager to rebuild from")

	require.Error(t, secondResult.err,
		"the waiting caller must be pushed onto the rebuild path, not handed the pool that just failed")
	assert.Nil(t, secondResult.conn)

	assert.Equal(t, int32(1), db.pingCount(), "the pair must cost one health check, not one each")
	assert.True(t, db.closed, "the pool that failed its health check must be closed")
}

// TestManager_GetConnection_ConcurrentCallersShareOnePassedCheck is the happy half of
// the same window: one check, both callers served the pool it passed.
func TestManager_GetConnection_ConcurrentCallersSharePassedCheck(t *testing.T) {
	t.Parallel()

	db := newBlockingDB()
	manager := newHealthIntervalManagerWithDB(t, db)

	first := resolveAsync(manager)

	<-db.started

	second := resolveAsync(manager)

	close(db.release)

	firstResult := <-first
	secondResult := <-second

	require.NoError(t, firstResult.err)
	require.NoError(t, secondResult.err)
	assert.Same(t, firstResult.conn, secondResult.conn, "both callers must get the pool that passed the check")
	assert.Equal(t, int32(1), db.pingCount(), "the pair must cost one health check, not one each")
	assert.False(t, db.closed, "a pool that passed its health check must stay open")
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
