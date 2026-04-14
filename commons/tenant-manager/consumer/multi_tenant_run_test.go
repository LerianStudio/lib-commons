// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/event"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/testutil"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/tenantcache"
)

// --------------------------------------------------------------------------
// Event-driven mode tests (T-004: consumer no longer creates/starts listener)
// --------------------------------------------------------------------------

func TestRun_NoListener(t *testing.T) {
	t.Parallel()

	server := setupTenantManagerAPIServer(t, makeTenantSummaries(5))
	config := newTestConfig(server.URL)

	consumer, err := NewMultiTenantConsumerWithError(config, testutil.NewMockLogger(), WithRabbitMQ(dummyRabbitMQManager()))
	require.NoError(t, err)

	ctx := context.Background()
	err = consumer.Run(ctx)
	require.NoError(t, err, "Run() should not return error")

	// Run() must NOT create an eventListener -- it is external now
	consumer.mu.RLock()
	hasParentCtx := consumer.parentCtx != nil
	consumer.mu.RUnlock()
	assert.True(t, hasParentCtx, "parentCtx should be set by Run()")

	require.NoError(t, consumer.Close())
}

func TestRun_EventDriven_EmptyTenantMap(t *testing.T) {
	t.Parallel()

	// API has 5 tenants -- but event-driven mode does NOT call discoverTenants
	server := setupTenantManagerAPIServer(t, makeTenantSummaries(5))
	config := newTestConfig(server.URL)

	consumer, err := NewMultiTenantConsumerWithError(config, testutil.NewMockLogger(), WithRabbitMQ(dummyRabbitMQManager()))
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
// Close tests (T-004: Close no longer stops a listener)
// --------------------------------------------------------------------------

func TestClose_NoListenerStop(t *testing.T) {
	t.Parallel()

	server := setupTenantManagerAPIServer(t, nil)
	config := newTestConfig(server.URL)

	consumer, err := NewMultiTenantConsumerWithError(config, testutil.NewMockLogger(), WithRabbitMQ(dummyRabbitMQManager()))
	require.NoError(t, err)

	ctx := context.Background()
	err = consumer.Run(ctx)
	require.NoError(t, err)

	// Close should work cleanly without needing to stop any listener
	err = consumer.Close()
	require.NoError(t, err, "Close() should not return error")

	stats := consumer.Stats()
	assert.True(t, stats.Closed, "consumer should be marked as closed")
}

// --------------------------------------------------------------------------
// WithEventDispatcher option test
// --------------------------------------------------------------------------

func TestWithEventDispatcher_SetsField(t *testing.T) {
	t.Parallel()

	server := setupTenantManagerAPIServer(t, nil)
	config := newTestConfig(server.URL)

	cache := tenantcache.NewTenantCache()
	dispatcher := event.NewEventDispatcher(cache, nil, "test-service")

	consumer, err := NewMultiTenantConsumerWithError(
		config,
		testutil.NewMockLogger(),
		WithRabbitMQ(dummyRabbitMQManager()),
		WithEventDispatcher(dispatcher),
	)
	require.NoError(t, err)
	assert.Equal(t, dispatcher, consumer.dispatcher, "dispatcher should be set via option")

	require.NoError(t, consumer.Close())
}

// --------------------------------------------------------------------------
// Constructor no longer requires redisClient or rabbitmq as positional param
// --------------------------------------------------------------------------

func TestNewMultiTenantConsumer_NoRedisRequired(t *testing.T) {
	t.Parallel()

	server := setupTenantManagerAPIServer(t, nil)
	config := newTestConfig(server.URL)

	// Constructor no longer takes redisClient -- only config, logger, opts
	consumer, err := NewMultiTenantConsumerWithError(
		config,
		testutil.NewMockLogger(),
		WithRabbitMQ(dummyRabbitMQManager()),
	)
	require.NoError(t, err, "constructor should succeed without redisClient")
	require.NotNil(t, consumer, "consumer should not be nil")

	require.NoError(t, consumer.Close())
}
