//go:build unit

package consumer

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/testutil"
	tmrabbitmq "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/rabbitmq"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestClientFor(t *testing.T, url string) (*client.Client, error) {
	t.Helper()

	return client.NewClient(url, testutil.NewMockLogger(),
		client.WithAllowInsecureHTTP(),
		client.WithServiceAPIKey("test-key"),
	)
}

func newTestConsumerConfig() MultiTenantConfig {
	cfg := DefaultMultiTenantConfig()
	cfg.MultiTenantURL = "http://localhost:8080"
	cfg.ServiceAPIKey = "test-key"
	cfg.Service = "ledger"
	cfg.AllowInsecureHTTP = true

	return cfg
}

// TestNewMultiTenantConsumerWithRabbitMQ_NilRabbitMQ covers the compat constructor (was 0%).
func TestNewMultiTenantConsumerWithRabbitMQ_NilRabbitMQ(t *testing.T) {
	t.Parallel()

	cfg := newTestConsumerConfig()
	logger := testutil.NewMockLogger()

	// nil rabbitmq - should still create consumer
	c, err := NewMultiTenantConsumerWithRabbitMQ(nil, cfg, logger)
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestNewMultiTenantConsumerWithRabbitMQ_WithRabbitMQ(t *testing.T) {
	t.Parallel()

	cfg := newTestConsumerConfig()
	logger := testutil.NewMockLogger()

	rmqClient, err := newTestClientFor(t, "http://localhost:8080")
	require.NoError(t, err)

	manager := tmrabbitmq.NewManager(rmqClient, "ledger")

	c, err := NewMultiTenantConsumerWithRabbitMQ(manager, cfg, logger)
	require.NoError(t, err)
	require.NotNil(t, c)
}

// TestNewMultiTenantConsumerWithRedis covers the compat constructor (was 0%).
func TestNewMultiTenantConsumerWithRedis_NilRabbitMQ(t *testing.T) {
	t.Parallel()

	cfg := newTestConsumerConfig()
	logger := testutil.NewMockLogger()

	c, err := NewMultiTenantConsumerWithRedis(nil, nil, cfg, logger)
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestNewMultiTenantConsumerWithRedis_WithRabbitMQ(t *testing.T) {
	t.Parallel()

	cfg := newTestConsumerConfig()
	logger := testutil.NewMockLogger()

	rmqClient, err := newTestClientFor(t, "http://localhost:8080")
	require.NoError(t, err)

	manager := tmrabbitmq.NewManager(rmqClient, "ledger")

	// redisClient is silently ignored
	c, err := NewMultiTenantConsumerWithRedis(manager, "ignored-redis-client", cfg, logger)
	require.NoError(t, err)
	require.NotNil(t, c)
}

// TestIsDegraded_NotInState covers IsDegraded when tenant has no retry state.
func TestIsDegraded_NotInRetryState(t *testing.T) {
	t.Parallel()

	cfg := newTestConsumerConfig()
	logger := testutil.NewMockLogger()

	c, err := NewMultiTenantConsumerWithError(cfg, logger)
	require.NoError(t, err)

	assert.False(t, c.IsDegraded("tenant-not-in-state"))
}

// TestWireDispatcherCallbacks covers the wireDispatcherCallbacks function.
func TestWireDispatcherCallbacks(t *testing.T) {
	t.Parallel()

	cfg := newTestConsumerConfig()
	logger := testutil.NewMockLogger()

	c, err := NewMultiTenantConsumerWithError(cfg, logger)
	require.NoError(t, err)

	// Verify dispatcher has callbacks wired by exercising the onTenantRemoved path
	// (won't panic; tenant is just not in knownTenants)
	assert.NotPanics(t, func() {
		c.StopConsumer("unknown-tenant")
	})
}

// TestRegister_NilRabbitMQ covers the nil RabbitMQ guard.
func TestRegister_NilRabbitMQ(t *testing.T) {
	t.Parallel()

	cfg := newTestConsumerConfig()
	logger := testutil.NewMockLogger()

	c, err := NewMultiTenantConsumerWithError(cfg, logger)
	require.NoError(t, err)

	err = c.Register("my-queue", func(_ context.Context, _ amqp.Delivery) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RabbitMQ manager is required")
}

// TestRegister_NilHandler covers the nil handler guard.
func TestRegister_NilHandler(t *testing.T) {
	t.Parallel()

	cfg := newTestConsumerConfig()
	logger := testutil.NewMockLogger()

	rmqClient, err := newTestClientFor(t, "http://localhost:8080")
	require.NoError(t, err)
	rmqManager := tmrabbitmq.NewManager(rmqClient, "ledger")

	c, err := NewMultiTenantConsumerWithError(cfg, logger, WithRabbitMQ(rmqManager))
	require.NoError(t, err)

	err = c.Register("my-queue", nil)
	require.Error(t, err)
}

// TestClose_EmptyConsumer covers Close on a freshly-created consumer.
func TestClose_EmptyConsumer(t *testing.T) {
	t.Parallel()

	cfg := newTestConsumerConfig()
	logger := testutil.NewMockLogger()

	c, err := NewMultiTenantConsumerWithError(cfg, logger)
	require.NoError(t, err)

	err = c.Close()
	require.NoError(t, err)
}

// TestGetRetryState_NoState covers getRetryState when no state exists.
func TestGetRetryState_NoState(t *testing.T) {
	t.Parallel()

	cfg := newTestConsumerConfig()
	logger := testutil.NewMockLogger()

	c, err := NewMultiTenantConsumerWithError(cfg, logger)
	require.NoError(t, err)

	// getRetryState should return a fresh state when none exists
	state := c.getRetryState("tenant-new")
	require.NotNil(t, state)
}

// TestResetRetryState covers resetRetryState.
func TestResetRetryState(t *testing.T) {
	t.Parallel()

	cfg := newTestConsumerConfig()
	logger := testutil.NewMockLogger()

	c, err := NewMultiTenantConsumerWithError(cfg, logger)
	require.NoError(t, err)

	// Create a retry state and increment it
	state := c.getRetryState("tenant-reset")
	require.NotNil(t, state)
	// Increment the retry state by marking degraded
	state.mu.Lock()
	state.retryCount = 5
	state.degraded = true
	state.mu.Unlock()

	// Verify it's degraded
	assert.True(t, c.IsDegraded("tenant-reset"))

	// Reset it
	c.resetRetryState("tenant-reset")

	// After reset, state should be fresh
	assert.False(t, c.IsDegraded("tenant-reset"))
}

// TestErrTenantServiceAccessDenied_IsCircuitBreakerOpenError checks the predicate functions
// exercised via the consumer (already covered but ensures package is built correctly).
func TestConsumerErrorPredicates(t *testing.T) {
	t.Parallel()

	assert.False(t, core.IsCircuitBreakerOpenError(nil))
	assert.True(t, core.IsCircuitBreakerOpenError(core.ErrCircuitBreakerOpen))
}

// TestBuildEventDispatcher_WithOptions covers buildEventDispatcher with various options.
func TestBuildEventDispatcher_WithOptions(t *testing.T) {
	t.Parallel()

	cfg := newTestConsumerConfig()
	logger := testutil.NewMockLogger()

	// Create consumer with postgres/mongo options to cover buildEventDispatcher branches
	rmqClient, err := newTestClientFor(t, "http://localhost:8080")
	require.NoError(t, err)

	rmqManager := tmrabbitmq.NewManager(rmqClient, "ledger")

	c, err := NewMultiTenantConsumerWithError(cfg, logger, WithRabbitMQ(rmqManager))
	require.NoError(t, err)
	require.NotNil(t, c)
	require.NotNil(t, c.dispatcher, "dispatcher should be built internally")
	require.NoError(t, c.Close())
}
