//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package mongo

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const healthIntervalTenant = "tenant-health-interval"

// newHealthIntervalManager returns a manager holding one cached client for
// healthIntervalTenant, pointed at a fake MongoDB server that counts the `ping`
// commands it answers. Async settings revalidation is switched off so the
// assertions observe health-check pings and nothing else.
func newHealthIntervalManager(t *testing.T, opts ...Option) (*Manager, *fakeMongoServer) {
	t.Helper()

	fakeDB, srv, cleanupFake := startCountingFakeMongoServer(t)

	base := []Option{
		WithLogger(testutil.NewMockLogger()),
		WithConnectionsCheckInterval(0),
	}

	manager := NewManager(nil, "ledger", append(base, opts...)...)

	t.Cleanup(func() {
		require.NoError(t, manager.Close(context.Background()))
		cleanupFake()
	})

	manager.mu.Lock()
	manager.connections[healthIntervalTenant] = &MongoConnection{DB: fakeDB}
	manager.mu.Unlock()

	return manager, srv
}

// getCachedConnection resolves the cached client n times, failing on any error.
func getCachedConnection(t *testing.T, manager *Manager, n int) {
	t.Helper()

	for range n {
		client, err := manager.GetConnection(context.Background(), healthIntervalTenant)
		require.NoError(t, err)
		require.NotNil(t, client)
	}
}

// TestManager_GetConnection_HealthCheckIsIntervalGated pins the cost of a cache
// hit: with the default interval, resolving the same tenant twice in a row must
// cost one health-check round trip, not one per call.
func TestManager_GetConnection_HealthCheckIsIntervalGated(t *testing.T) {
	t.Parallel()

	manager, srv := newHealthIntervalManager(t)

	getCachedConnection(t, manager, 2)

	assert.Equal(t, int32(1), srv.pings.Load(),
		"a cache hit inside the health-check window must not ping the tenant database")
}

// TestManager_GetConnection_HealthCheckRunsAfterInterval proves the gate is a
// delay and not a mute: once the window has elapsed, the next resolution pings.
func TestManager_GetConnection_HealthCheckRunsAfterInterval(t *testing.T) {
	t.Parallel()

	manager, srv := newHealthIntervalManager(t, WithHealthCheckInterval(time.Nanosecond))

	getCachedConnection(t, manager, 2)

	assert.Equal(t, int32(2), srv.pings.Load(),
		"a cache hit after the health-check window must ping the tenant database again")
}

// newBlockingHealthIntervalManager caches a client whose every health-check ping is
// held by the fake server until the test releases it, so a check can be kept in
// flight while a second caller resolves the same tenant.
func newBlockingHealthIntervalManager(t *testing.T, opts ...Option) (*Manager, *fakeMongoServer, *MongoConnection) {
	t.Helper()

	fakeDB, srv, cleanupFake := startBlockingFakeMongoServer(t)

	base := []Option{
		WithLogger(testutil.NewMockLogger()),
		WithConnectionsCheckInterval(0),
	}

	manager := NewManager(nil, "ledger", append(base, opts...)...)

	t.Cleanup(func() {
		require.NoError(t, manager.Close(context.Background()))
		cleanupFake()
	})

	conn := &MongoConnection{DB: fakeDB}

	manager.mu.Lock()
	manager.connections[healthIntervalTenant] = conn
	manager.mu.Unlock()

	return manager, srv, conn
}

// clientResult is what a concurrent GetConnection call handed back.
type clientResult struct {
	client *mongo.Client
	err    error
}

// resolveAsync resolves healthIntervalTenant on its own goroutine.
func resolveAsync(manager *Manager) <-chan clientResult {
	out := make(chan clientResult, 1)

	go func() {
		client, err := manager.GetConnection(context.Background(), healthIntervalTenant)
		out <- clientResult{client: client, err: err}
	}()

	return out
}

// TestManager_GetConnection_ConcurrentCallerWaitsForFailedCheck is the client-pulled-
// from-under-you case: while one caller's health check is in flight, a second caller
// for the same tenant must not be handed that client, because the check may still
// condemn it. Before the interval gate every caller ran its own check and saw the
// failure itself; a caller that skips the check must therefore wait for the verdict.
func TestManager_GetConnection_ConcurrentCallerWaitsForFailedCheck(t *testing.T) {
	t.Parallel()

	manager, srv, _ := newBlockingHealthIntervalManager(t)

	first := resolveAsync(manager)

	<-srv.started // the health check is in flight

	second := resolveAsync(manager)

	select {
	case got := <-second:
		t.Fatalf("second caller returned before the in-flight health check resolved: client=%p err=%v", got.client, got.err)
	case <-time.After(100 * time.Millisecond):
	}

	srv.failPing.Store(true)
	close(srv.gate)

	firstResult := <-first
	secondResult := <-second

	require.Error(t, firstResult.err, "the caller that ran the failed check has no Tenant Manager to rebuild from")

	require.Error(t, secondResult.err,
		"the waiting caller must be pushed onto the rebuild path, not handed the client that just failed")
	assert.Nil(t, secondResult.client)

	assert.Equal(t, int32(1), srv.pings.Load(), "the pair must cost one health check, not one each")

	manager.mu.RLock()
	_, cached := manager.connections[healthIntervalTenant]
	manager.mu.RUnlock()

	assert.False(t, cached, "the client that failed its health check must be evicted")
}

// TestManager_GetConnection_ConcurrentCallersSharePassedCheck is the happy half of
// the same window: one check, both callers served the client it passed.
func TestManager_GetConnection_ConcurrentCallersSharePassedCheck(t *testing.T) {
	t.Parallel()

	manager, srv, conn := newBlockingHealthIntervalManager(t)

	first := resolveAsync(manager)

	<-srv.started

	second := resolveAsync(manager)

	close(srv.gate)

	firstResult := <-first
	secondResult := <-second

	require.NoError(t, firstResult.err)
	require.NoError(t, secondResult.err)
	assert.Same(t, conn.DB, firstResult.client)
	assert.Same(t, conn.DB, secondResult.client, "both callers must get the client that passed the check")
	assert.Equal(t, int32(1), srv.pings.Load(), "the pair must cost one health check, not one each")
}

// TestManager_GetConnection_UnhealthyCacheEvicts proves the interval gate did not
// soften the failure path: when a due health check fails, the cached client is
// evicted and the caller is pushed onto the rebuild path. Mongo had no coverage
// for this; postgres asserts the same thing in TestManager_GetConnection_UnhealthyCacheEvicts.
func TestManager_GetConnection_UnhealthyCacheEvicts(t *testing.T) {
	t.Parallel()

	// A client pointed at a port nobody is listening on: connect is lazy, so the
	// failure surfaces on the health-check ping.
	deadClient, err := mongo.Connect(options.Client().
		ApplyURI("mongodb://127.0.0.1:1/?directConnection=true").
		SetServerSelectionTimeout(100 * time.Millisecond).
		SetConnectTimeout(100 * time.Millisecond))
	require.NoError(t, err)

	t.Cleanup(func() { _ = deadClient.Disconnect(context.Background()) })

	manager := NewManager(nil, "ledger",
		WithLogger(testutil.NewMockLogger()),
		WithConnectionsCheckInterval(0),
		WithHealthCheckInterval(0),
	)
	t.Cleanup(func() { require.NoError(t, manager.Close(context.Background())) })

	manager.mu.Lock()
	manager.connections[healthIntervalTenant] = &MongoConnection{DB: deadClient}
	manager.mu.Unlock()

	// The rebuild fails (no Tenant Manager client configured), but the eviction is
	// the assertion: the dead client must be gone from the cache.
	client, err := manager.GetConnection(context.Background(), healthIntervalTenant)
	require.Error(t, err)
	assert.Nil(t, client)

	manager.mu.RLock()
	_, cached := manager.connections[healthIntervalTenant]
	manager.mu.RUnlock()

	assert.False(t, cached, "a client that failed its health check must be evicted from the cache")
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

			manager, srv := newHealthIntervalManager(t, WithHealthCheckInterval(interval))

			getCachedConnection(t, manager, 3)

			assert.Equal(t, int32(3), srv.pings.Load(),
				"a non-positive health-check interval must ping on every cache hit")
		})
	}
}
