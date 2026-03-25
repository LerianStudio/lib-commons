package consumer

import (
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
)

// --------------------------------------------------------------------------
// RedisClient constructor parameter tests (now required, not an option)
// --------------------------------------------------------------------------

func TestNewMultiTenantConsumer_NilRedisClient_Rejected(t *testing.T) {
	t.Parallel()

	server := setupTenantManagerAPIServer(t, nil)

	consumer, err := NewMultiTenantConsumerWithError(
		dummyRabbitMQManager(),
		nil, // nil redisClient
		newTestConfig(server.URL),
		testutil.NewMockLogger(),
	)

	require.Error(t, err, "nil redisClient should be rejected")
	assert.Nil(t, consumer, "consumer should be nil when nil redisClient is passed")
	assert.Contains(t, err.Error(), "redisClient must not be nil")
}

func TestNewMultiTenantConsumer_TypedNilRedisClient_Rejected(t *testing.T) {
	t.Parallel()

	// Create a typed-nil: a *redis.Client that is nil, wrapped in the UniversalClient interface.
	var typedNil *redis.Client

	server := setupTenantManagerAPIServer(t, nil)

	consumer, err := NewMultiTenantConsumerWithError(
		dummyRabbitMQManager(),
		typedNil,
		newTestConfig(server.URL),
		testutil.NewMockLogger(),
	)

	require.Error(t, err, "typed-nil redisClient should be rejected")
	assert.Nil(t, consumer, "consumer should be nil when typed-nil redisClient is passed")
	assert.Contains(t, err.Error(), "redisClient must not be nil")
}

func TestNewMultiTenantConsumer_ValidRedisClient_Accepted(t *testing.T) {
	t.Parallel()

	rc := testRedisClient(t)
	server := setupTenantManagerAPIServer(t, nil)

	consumer, err := NewMultiTenantConsumerWithError(
		dummyRabbitMQManager(),
		rc,
		newTestConfig(server.URL),
		testutil.NewMockLogger(),
	)

	require.NoError(t, err, "valid redisClient should be accepted")
	require.NotNil(t, consumer, "consumer should not be nil")
	assert.NotNil(t, consumer.redisClient, "redisClient should be set on consumer")
	assert.Nil(t, consumer.eventListener, "eventListener should be nil until Run() is called")

	require.NoError(t, consumer.Close())
}
