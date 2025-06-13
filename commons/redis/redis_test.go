package redis

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
)

func TestRedisConnection_Connect(t *testing.T) {
	// Start a mini Redis server for testing
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("Failed to start miniredis: %v", err)
	}
	defer mr.Close()

	// Create logger
	logger := &log.GoLogger{Level: log.InfoLevel}

	tests := []struct {
		name        string
		redisConn   *RedisConnection
		expectError bool
	}{
		{
			name: "successful connection",
			redisConn: &RedisConnection{
				Addr:   mr.Addr(),
				Logger: logger,
			},
			expectError: false,
		},
		{
			name: "failed connection - wrong address",
			redisConn: &RedisConnection{
				Addr:   "wrong_address:6379",
				Logger: logger,
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			err := tt.redisConn.Connect(ctx)

			if tt.expectError {
				assert.Error(t, err)
				assert.False(t, tt.redisConn.Connected)
				assert.Nil(t, tt.redisConn.Client)
			} else {
				assert.NoError(t, err)
				assert.True(t, tt.redisConn.Connected)
				assert.NotNil(t, tt.redisConn.Client)
			}
		})
	}
}

func TestRedisConnection_GetClient(t *testing.T) {
	// Start a mini Redis server for testing
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("Failed to start miniredis: %v", err)
	}
	defer mr.Close()

	// Create logger
	logger := &log.GoLogger{Level: log.InfoLevel}

	t.Run("get client - first time initialization", func(t *testing.T) {
		ctx := context.Background()
		redisConn := &RedisConnection{
			Addr:   mr.Addr(),
			Logger: logger,
		}

		client, err := redisConn.GetClient(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, client)
		assert.True(t, redisConn.Connected)
	})

	t.Run("get client - already initialized", func(t *testing.T) {
		ctx := context.Background()
		redisConn := &RedisConnection{
			Addr:   mr.Addr(),
			Logger: logger,
		}

		// First call to initialize
		_, err := redisConn.GetClient(ctx)
		assert.NoError(t, err)

		// Second call to get existing client
		client, err := redisConn.GetClient(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, client)
		assert.True(t, redisConn.Connected)
	})

	t.Run("get client - connection fails", func(t *testing.T) {
		ctx := context.Background()
		redisConn := &RedisConnection{
			Addr:   "wrong_address:6379",
			Logger: logger,
		}

		client, err := redisConn.GetClient(ctx)
		assert.Error(t, err)
		assert.Nil(t, client)
		assert.False(t, redisConn.Connected)
	})
}

func TestRedisIntegration(t *testing.T) {
	// Skip this test when running in CI environment
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Start a mini Redis server for testing
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("Failed to start miniredis: %v", err)
	}
	defer mr.Close()

	// Create logger
	logger := &log.GoLogger{Level: log.InfoLevel}

	// Create Redis connection
	redisConn := &RedisConnection{
		Addr:   mr.Addr(),
		Logger: logger,
	}

	ctx := context.Background()
	
	// Connect to Redis
	err = redisConn.Connect(ctx)
	assert.NoError(t, err)

	// Get client
	client, err := redisConn.GetClient(ctx)
	assert.NoError(t, err)

	// Test setting and getting a value
	key := "test_key"
	value := "test_value"
	
	err = client.Set(ctx, key, value, 0).Err()
	assert.NoError(t, err)
	
	result, err := client.Get(ctx, key).Result()
	assert.NoError(t, err)
	assert.Equal(t, value, result)
}
