// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
)

// --------------------------------------------------------------------------
// Event-driven mode tests
// --------------------------------------------------------------------------

func TestRun_EventDrivenMode(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rc.Close() })

	server := setupTenantManagerAPIServer(t, makeTenantSummaries(5))
	config := newTestConfig(server.URL)

	consumer, err := NewMultiTenantConsumerWithError(dummyRabbitMQManager(), rc, config, testutil.NewMockLogger())
	require.NoError(t, err)

	ctx := context.Background()
	err = consumer.Run(ctx)
	require.NoError(t, err, "Run() should not return error")

	// Event listener must be created and started
	consumer.mu.RLock()
	hasListener := consumer.eventListener != nil
	consumer.mu.RUnlock()
	assert.True(t, hasListener, "eventListener should be created")

	require.NoError(t, consumer.Close())
}

func TestRun_EventDriven_EmptyTenantMap(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rc.Close() })

	// API has 5 tenants -- but event-driven mode does NOT call discoverTenants
	server := setupTenantManagerAPIServer(t, makeTenantSummaries(5))
	config := newTestConfig(server.URL)

	consumer, err := NewMultiTenantConsumerWithError(dummyRabbitMQManager(), rc, config, testutil.NewMockLogger())
	require.NoError(t, err)

	ctx := context.Background()
	err = consumer.Run(ctx)
	require.NoError(t, err)

	// Tenant map must be empty -- no discovery, no eager start
	stats := consumer.Stats()
	assert.Equal(t, 0, stats.KnownTenants, "knownTenants should be empty (lazy-load)")
	assert.Equal(t, 0, stats.ActiveTenants, "activeTenants should be 0 (no eager start)")

	require.NoError(t, consumer.Close())
}

// --------------------------------------------------------------------------
// Close tests
// --------------------------------------------------------------------------

func TestClose_EventDrivenMode(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rc.Close() })

	server := setupTenantManagerAPIServer(t, nil)
	config := newTestConfig(server.URL)

	consumer, err := NewMultiTenantConsumerWithError(dummyRabbitMQManager(), rc, config, testutil.NewMockLogger())
	require.NoError(t, err)

	ctx := context.Background()
	err = consumer.Run(ctx)
	require.NoError(t, err)

	// Verify listener is running
	consumer.mu.RLock()
	hasListener := consumer.eventListener != nil
	consumer.mu.RUnlock()
	require.True(t, hasListener, "event listener must be running before Close()")

	// Close should stop the event listener
	err = consumer.Close()
	require.NoError(t, err, "Close() should not return error")

	stats := consumer.Stats()
	assert.True(t, stats.Closed, "consumer should be marked as closed")
}
