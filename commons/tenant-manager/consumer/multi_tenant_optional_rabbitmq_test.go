// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/goleak"
)

// ---------------------
// T-006: Make RabbitMQ optional in MultiTenantConsumer
// ---------------------

// TestNewMultiTenantConsumer_NoRabbitMQ verifies that the constructor succeeds
// without RabbitMQ when no WithRabbitMQ option is provided.
func TestNewMultiTenantConsumer_NoRabbitMQ(t *testing.T) {
	t.Parallel()

	server := setupTenantManagerAPIServer(t, nil)
	config := newTestConfig(server.URL)

	// Constructor should succeed without RabbitMQ (HTTP-only mode)
	consumer, err := NewMultiTenantConsumerWithError(config, testutil.NewMockLogger())
	require.NoError(t, err, "constructor should succeed without RabbitMQ")
	require.NotNil(t, consumer, "consumer must not be nil")

	// RabbitMQ should be nil
	assert.Nil(t, consumer.rabbitmq, "rabbitmq should be nil when not provided")

	// Core fields should be initialized
	assert.NotNil(t, consumer.handlers, "handlers map must be initialized")
	assert.NotNil(t, consumer.tenants, "tenants map must be initialized")
	assert.NotNil(t, consumer.knownTenants, "knownTenants map must be initialized")
	assert.NotNil(t, consumer.pmClient, "pmClient must be initialized")
	assert.NotNil(t, consumer.loader, "loader must be initialized")
	assert.NotNil(t, consumer.dispatcher, "dispatcher must be initialized")

	require.NoError(t, consumer.Close())
}

// TestNewMultiTenantConsumer_WithRabbitMQ verifies that the WithRabbitMQ option
// correctly sets the rabbitmq field on the consumer.
func TestNewMultiTenantConsumer_WithRabbitMQ(t *testing.T) {
	t.Parallel()

	server := setupTenantManagerAPIServer(t, nil)
	config := newTestConfig(server.URL)

	rmq := dummyRabbitMQManager()

	consumer, err := NewMultiTenantConsumerWithError(
		config,
		testutil.NewMockLogger(),
		WithRabbitMQ(rmq),
	)
	require.NoError(t, err, "constructor should succeed with WithRabbitMQ option")
	require.NotNil(t, consumer, "consumer must not be nil")
	assert.Equal(t, rmq, consumer.rabbitmq, "rabbitmq field should be set via WithRabbitMQ option")

	require.NoError(t, consumer.Close())
}

// TestRegister_NoRabbitMQ_ReturnsError verifies that Register() returns an error
// when RabbitMQ is not set (HTTP-only mode).
func TestRegister_NoRabbitMQ_ReturnsError(t *testing.T) {
	t.Parallel()

	server := setupTenantManagerAPIServer(t, nil)
	config := newTestConfig(server.URL)

	// Create consumer without RabbitMQ
	consumer, err := NewMultiTenantConsumerWithError(config, testutil.NewMockLogger())
	require.NoError(t, err)

	// Register should return error when no RabbitMQ
	err = consumer.Register("test-queue", func(ctx context.Context, delivery amqp.Delivery) error {
		return nil
	})
	require.Error(t, err, "Register should fail without RabbitMQ")
	assert.Contains(t, err.Error(), "RabbitMQ manager is required",
		"error should mention RabbitMQ requirement")
	assert.Contains(t, err.Error(), "WithRabbitMQ",
		"error should mention WithRabbitMQ option")

	require.NoError(t, consumer.Close())
}

// TestEnsureConsumerStarted_NoRabbitMQ_SkipsGoroutine verifies that
// ensureConsumerStarted lazy-loads the tenant but does NOT spawn a consumer
// goroutine when RabbitMQ is not set.
func TestEnsureConsumerStarted_NoRabbitMQ_SkipsGoroutine(t *testing.T) {
	t.Parallel()

	tenantID := "tenant-http-only-001"
	config := newTestTenantConfig(tenantID)

	var requestCount atomic.Int64
	server := setupLazyLoadServer(t, tenantID, config, &requestCount)

	// Create consumer without RabbitMQ
	consumer, err := NewMultiTenantConsumerWithError(
		newTestConfig(server.URL),
		testutil.NewMockLogger(),
	)
	require.NoError(t, err)

	// Set parentCtx (normally done by Run())
	ctx := context.Background()
	consumer.mu.Lock()
	consumer.parentCtx = ctx
	consumer.mu.Unlock()

	t.Cleanup(func() { consumer.Close() })

	// Trigger lazy-load for unknown tenant
	consumer.ensureConsumerStarted(ctx, tenantID)

	// Verify: tenant was lazy-loaded (HTTP request made)
	assert.Equal(t, int64(1), requestCount.Load(),
		"ensureConsumerStarted should still lazy-load from API")

	// Verify: tenant is marked as known
	consumer.mu.RLock()
	known := consumer.knownTenants[tenantID]
	consumer.mu.RUnlock()
	assert.True(t, known, "tenant should be marked as known after lazy-load")

	// Verify: tenant is cached
	entry, cached := consumer.cache.Get(tenantID)
	assert.True(t, cached, "tenant should be in cache after lazy-load")
	if cached && entry != nil {
		assert.Equal(t, tenantID, entry.Config.ID, "cached config ID should match")
	}

	// Verify: NO consumer goroutine was spawned (HTTP-only mode)
	consumer.mu.RLock()
	_, hasConsumer := consumer.tenants[tenantID]
	consumer.mu.RUnlock()
	assert.False(t, hasConsumer,
		"no consumer goroutine should be spawned when RabbitMQ is nil")

	// Verify: no goroutine leaks
	goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/cache.(*InMemoryCache).cleanupLoop"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreTopFunction("testing.tRunner.func1"),
		goleak.IgnoreTopFunction("testing.(*M).Run"),
	)
}
