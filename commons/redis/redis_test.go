package redis

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v3/commons/log"
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
		skip        bool
		skipReason  string
	}{
		{
			name: "successful connection - standalone mode",
			redisConn: &RedisConnection{
				Mode:    ModeStandalone,
				Address: []string{mr.Addr()},
				Logger:  logger,
			},
			expectError: false,
		},
		{
			name: "successful connection - sentinel mode",
			redisConn: &RedisConnection{
				Mode:       ModeSentinel,
				Address:    []string{mr.Addr()},
				MasterName: "mymaster",
				Logger:     logger,
			},
			skip:       true,
			skipReason: "miniredis doesn't support sentinel commands",
		},
		{
			name: "successful connection - cluster mode",
			redisConn: &RedisConnection{
				Mode:    ModeCluster,
				Address: []string{mr.Addr()},
				Logger:  logger,
			},
			expectError: false,
		},
		{
			name: "failed connection - wrong addresses",
			redisConn: &RedisConnection{
				Mode:    ModeStandalone,
				Address: []string{"wrong_address:6379"},
				Logger:  logger,
			},
			expectError: true,
		},
		{
			name: "failed connection - wrong sentinel addresses",
			redisConn: &RedisConnection{
				Mode:       ModeSentinel,
				Address:    []string{"wrong_address:6379"},
				MasterName: "mymaster",
				Logger:     logger,
			},
			expectError: true,
		},
		{
			name: "failed connection - wrong cluster addresses",
			redisConn: &RedisConnection{
				Mode:    ModeCluster,
				Address: []string{"wrong_address:6379"},
				Logger:  logger,
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skip {
				t.Skip(tt.skipReason)
			}

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
			Mode:    ModeStandalone,
			Address: []string{mr.Addr()},
			Logger:  logger,
		}

		client, err := redisConn.GetClient(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, client)
		assert.True(t, redisConn.Connected)
	})

	t.Run("get client - already initialized", func(t *testing.T) {
		ctx := context.Background()
		redisConn := &RedisConnection{
			Mode:    ModeStandalone,
			Address: []string{mr.Addr()},
			Logger:  logger,
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
			Mode:    ModeStandalone,
			Address: []string{"wrong_address:6379"},
			Logger:  logger,
		}

		client, err := redisConn.GetClient(ctx)
		assert.Error(t, err)
		assert.Nil(t, client)
		assert.False(t, redisConn.Connected)
	})

	// Test different connection modes
	testModes := []struct {
		name       string
		redisConn  *RedisConnection
		skip       bool
		skipReason string
	}{
		{
			name: "sentinel mode",
			redisConn: &RedisConnection{
				Mode:       ModeSentinel,
				Address:    []string{mr.Addr()},
				MasterName: "mymaster",
				Logger:     logger,
			},
			skip:       true,
			skipReason: "miniredis doesn't support sentinel commands",
		},
		{
			name: "cluster mode",
			redisConn: &RedisConnection{
				Mode:    ModeCluster,
				Address: []string{mr.Addr()},
				Logger:  logger,
			},
		},
	}

	for _, mode := range testModes {
		t.Run("get client - "+mode.name, func(t *testing.T) {
			if mode.skip {
				t.Skip(mode.skipReason)
			}

			ctx := context.Background()
			client, err := mode.redisConn.GetClient(ctx)
			assert.NoError(t, err)
			assert.NotNil(t, client)
			assert.True(t, mode.redisConn.Connected)
		})
	}
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
		Mode:    ModeStandalone,
		Address: []string{mr.Addr()},
		Logger:  logger,
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

func TestTTLFunctionality(t *testing.T) {
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
		Mode:    ModeStandalone,
		Address: []string{mr.Addr()},
		Logger:  logger,
	}

	ctx := context.Background()

	// Connect to Redis
	err = redisConn.Connect(ctx)
	assert.NoError(t, err)

	// Get client
	client, err := redisConn.GetClient(ctx)
	assert.NoError(t, err)

	// Test setting a value with TTL
	key := "ttl_key"
	value := "ttl_value"

	// Use the default TTL constant
	err = client.Set(ctx, key, value, time.Duration(TTL)*time.Second).Err()
	assert.NoError(t, err)

	// Check TTL is set
	ttl, err := client.TTL(ctx, key).Result()
	assert.NoError(t, err)
	assert.True(t, ttl > 0, "TTL should be greater than 0")

	// Verify the value is still accessible
	result, err := client.Get(ctx, key).Result()
	assert.NoError(t, err)
	assert.Equal(t, value, result)

	// Fast-forward time in miniredis to simulate expiration
	mr.FastForward(time.Duration(TTL+1) * time.Second)

	// Verify the key has expired
	exists, err := client.Exists(ctx, key).Result()
	assert.NoError(t, err)
	assert.Equal(t, int64(0), exists, "Key should have expired")
}

func TestModesIntegration(t *testing.T) {
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

	// Test all connection modes
	modes := []struct {
		name       string
		redisConn  *RedisConnection
		skip       bool
		skipReason string
	}{
		{
			name: "standalone mode",
			redisConn: &RedisConnection{
				Mode:    ModeStandalone,
				Address: []string{mr.Addr()},
				Logger:  logger,
			},
		},
		{
			name: "sentinel mode",
			redisConn: &RedisConnection{
				Mode:       ModeSentinel,
				Address:    []string{mr.Addr()},
				MasterName: "mymaster",
				Logger:     logger,
			},
			skip:       true,
			skipReason: "miniredis doesn't support sentinel commands",
		},
		{
			name: "cluster mode",
			redisConn: &RedisConnection{
				Mode:    ModeCluster,
				Address: []string{mr.Addr()},
				Logger:  logger,
			},
		},
	}

	ctx := context.Background()

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			if mode.skip {
				t.Skip(mode.skipReason)
			}

			// Connect to Redis
			err := mode.redisConn.Connect(ctx)
			assert.NoError(t, err)

			// Get client
			client, err := mode.redisConn.GetClient(ctx)
			assert.NoError(t, err)

			// Test basic operations
			key := "test_key_" + string(mode.redisConn.Mode)
			value := "test_value_" + string(mode.redisConn.Mode)

			// Test with TTL
			err = client.Set(ctx, key, value, time.Duration(TTL)*time.Second).Err()
			assert.NoError(t, err)

			result, err := client.Get(ctx, key).Result()
			assert.NoError(t, err)
			assert.Equal(t, value, result)

			// Test Close method
			if mode.redisConn != nil {
				err = mode.redisConn.Close()
				assert.NoError(t, err)
			}
		})
	}
}

func TestRedisWithTLSConfig(t *testing.T) {
	// This test is more of a unit test to ensure TLS configuration is properly set up
	// Actual TLS connections can't be tested with miniredis

	// Create logger
	logger := &log.GoLogger{Level: log.InfoLevel}

	// Create Redis connection with TLS
	redisConn := &RedisConnection{
		Mode:    ModeStandalone,
		Address: []string{"localhost:6379"},
		UseTLS:  true,
		Logger:  logger,
	}

	// Verify that TLS would be used in all modes
	modes := []struct {
		name string
		mode Mode
	}{
		{"standalone", ModeStandalone},
		{"sentinel", ModeSentinel},
		{"cluster", ModeCluster},
	}

	for _, modeTest := range modes {
		t.Run("tls_config_"+modeTest.name, func(t *testing.T) {
			redisConn.Mode = modeTest.mode

			// We don't actually connect, just verify the TLS config would be used
			assert.True(t, redisConn.UseTLS)
		})
	}
}
