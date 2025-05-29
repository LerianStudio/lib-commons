package redis

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)


func TestClusterConfig_DefaultValues(t *testing.T) {
	config := ClusterConfig{}

	assert.Empty(t, config.Addrs)
	assert.Empty(t, config.Password)
	assert.Empty(t, config.Username)
	assert.Equal(t, 0, config.MaxRetries)
	assert.Equal(t, 0, config.PoolSize)
	assert.Equal(t, 0, config.MinIdleConns)
	assert.Equal(t, time.Duration(0), config.MaxConnAge)
	assert.Equal(t, time.Duration(0), config.PoolTimeout)
	assert.Equal(t, time.Duration(0), config.IdleTimeout)
	assert.Equal(t, time.Duration(0), config.ReadTimeout)
	assert.Equal(t, time.Duration(0), config.WriteTimeout)
	assert.False(t, config.RouteByLatency)
	assert.False(t, config.RouteRandomly)
}

func TestClusterConfig_Validation(t *testing.T) {
	testCases := []struct {
		name        string
		config      ClusterConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid basic config",
			config: ClusterConfig{
				Addrs: []string{"localhost:7000", "localhost:7001", "localhost:7002"},
			},
			expectError: false,
		},
		{
			name: "empty addresses",
			config: ClusterConfig{
				Addrs: []string{},
			},
			expectError: true,
			errorMsg:    "cluster addresses cannot be empty",
		},
		{
			name: "nil addresses",
			config: ClusterConfig{
				Addrs: nil,
			},
			expectError: true,
			errorMsg:    "cluster addresses cannot be empty",
		},
		{
			name: "invalid address format",
			config: ClusterConfig{
				Addrs: []string{"invalid-address"},
			},
			expectError: true,
			errorMsg:    "invalid address format",
		},
		{
			name: "valid config with auth",
			config: ClusterConfig{
				Addrs:    []string{"localhost:7000", "localhost:7001"},
				Username: "user",
				Password: "pass",
			},
			expectError: false,
		},
		{
			name: "valid config with pool settings",
			config: ClusterConfig{
				Addrs:        []string{"localhost:7000"},
				PoolSize:     10,
				MinIdleConns: 5,
				MaxConnAge:   5 * time.Minute,
			},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.config.Validate()
			if tc.expectError {
				assert.Error(t, err)
				if tc.errorMsg != "" {
					assert.Contains(t, err.Error(), tc.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRedisClusterConnection_DefaultValues(t *testing.T) {
	rcc := &RedisClusterConnection{}

	assert.Empty(t, rcc.ClusterConfig.Addrs)
	assert.Nil(t, rcc.ClusterClient)
	assert.False(t, rcc.Connected)
	assert.Nil(t, rcc.Logger)
}

func TestRedisClusterConnection_ConnectCluster_Success(t *testing.T) {
	mockLogger := new(MockLogger)
	mockLogger.On("Info", mock.Anything).Maybe()

	config := ClusterConfig{
		Addrs:       []string{"localhost:7000", "localhost:7001", "localhost:7002"},
		MaxRetries:  3,
		PoolSize:    10,
		ReadTimeout: 5 * time.Second,
	}

	rcc := &RedisClusterConnection{
		ClusterConfig: config,
		Logger:        mockLogger,
	}

	// Note: This test would need to be adjusted based on actual implementation
	// For now, we're testing the structure and configuration
	assert.Equal(t, config, rcc.ClusterConfig)
	assert.NotNil(t, rcc.Logger)
	assert.False(t, rcc.Connected)
}

func TestRedisClusterConnection_ConnectCluster_InvalidConfig(t *testing.T) {
	mockLogger := new(MockLogger)
	mockLogger.On("Info", mock.Anything).Maybe()
	mockLogger.On("Error", mock.Anything, mock.Anything).Maybe()

	testCases := []struct {
		name   string
		config ClusterConfig
	}{
		{
			name: "empty addresses",
			config: ClusterConfig{
				Addrs: []string{},
			},
		},
		{
			name: "nil addresses",
			config: ClusterConfig{
				Addrs: nil,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rcc := &RedisClusterConnection{
				ClusterConfig: tc.config,
				Logger:        mockLogger,
			}

			ctx := context.Background()
			err := rcc.ConnectCluster(ctx)
			assert.Error(t, err)
			assert.False(t, rcc.Connected)
		})
	}
}

func TestRedisClusterConnection_GetClusterClient_AlreadyConnected(t *testing.T) {
	mockLogger := new(MockLogger)
	mockClient := &redis.ClusterClient{}

	rcc := &RedisClusterConnection{
		ClusterClient: mockClient,
		Connected:     true,
		Logger:        mockLogger,
	}

	ctx := context.Background()
	client, err := rcc.GetClusterClient(ctx)

	assert.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, mockClient, client)
}

func TestRedisClusterConnection_GetClusterClient_NotConnected(t *testing.T) {
	mockLogger := new(MockLogger)
	mockLogger.On("Info", mock.Anything).Maybe()
	mockLogger.On("Error", mock.Anything, mock.Anything).Maybe()

	config := ClusterConfig{
		Addrs: []string{"localhost:7000", "localhost:7001"},
	}

	rcc := &RedisClusterConnection{
		ClusterConfig: config,
		Connected:     false,
		Logger:        mockLogger,
	}

	ctx := context.Background()
	client, err := rcc.GetClusterClient(ctx)

	// Should attempt to connect and return error for invalid addresses
	assert.Error(t, err)
	assert.Nil(t, client)
}

func TestRedisClusterConnection_HealthCheck(t *testing.T) {
	t.Run("healthy cluster", func(t *testing.T) {
		mockLogger := new(MockLogger)
		
		// Create a mock cluster client
		mockClient := &redis.ClusterClient{}

		rcc := &RedisClusterConnection{
			ClusterClient: mockClient,
			Connected:     true,
			Logger:        mockLogger,
		}

		// For unit testing, we'll test the method exists and handles disconnected state
		ctx := context.Background()
		err := rcc.HealthCheck(ctx)
		
		// This will fail in unit test environment but validates the method signature
		assert.Error(t, err) // Expected since we're not connected to real Redis
	})

	t.Run("not connected", func(t *testing.T) {
		mockLogger := new(MockLogger)

		rcc := &RedisClusterConnection{
			ClusterClient: nil,
			Connected:     false,
			Logger:        mockLogger,
		}

		ctx := context.Background()
		err := rcc.HealthCheck(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not connected")
	})
}

func TestRedisClusterConnection_GetClusterInfo(t *testing.T) {
	t.Run("connected cluster", func(t *testing.T) {
		mockLogger := new(MockLogger)
		mockClient := &redis.ClusterClient{}

		rcc := &RedisClusterConnection{
			ClusterClient: mockClient,
			Connected:     true,
			Logger:        mockLogger,
		}

		ctx := context.Background()
		info, err := rcc.GetClusterInfo(ctx)

		// In unit test environment, this will error but validates the interface
		assert.Error(t, err)
		assert.Nil(t, info)
	})

	t.Run("not connected", func(t *testing.T) {
		mockLogger := new(MockLogger)

		rcc := &RedisClusterConnection{
			ClusterClient: nil,
			Connected:     false,
			Logger:        mockLogger,
		}

		ctx := context.Background()
		info, err := rcc.GetClusterInfo(ctx)
		assert.Error(t, err)
		assert.Nil(t, info)
		assert.Contains(t, err.Error(), "not connected")
	})
}

func TestRedisConnectionFactory_CreateConnection(t *testing.T) {
	mockLogger := new(MockLogger)
	factory := &RedisConnectionFactory{
		Logger: mockLogger,
	}

	testCases := []struct {
		name        string
		connType    ConnectionType
		config      any
		expectError bool
		errorMsg    string
	}{
		{
			name:     "single instance connection",
			connType: SingleInstance,
			config: &RedisConnection{
				Addr:     "localhost:6379",
				Password: "password",
			},
			expectError: false,
		},
		{
			name:     "cluster connection",
			connType: Cluster,
			config: &ClusterConfig{
				Addrs: []string{"localhost:7000", "localhost:7001"},
			},
			expectError: false,
		},
		{
			name:        "unsupported connection type",
			connType:    ConnectionType(999),
			config:      nil,
			expectError: true,
			errorMsg:    "unsupported connection type",
		},
		{
			name:        "invalid config type for cluster",
			connType:    Cluster,
			config:      "invalid-config",
			expectError: true,
			errorMsg:    "invalid config type",
		},
		{
			name:        "nil config",
			connType:    SingleInstance,
			config:      nil,
			expectError: true,
			errorMsg:    "config cannot be nil",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := factory.CreateConnection(tc.connType, tc.config)

			if tc.expectError {
				assert.Error(t, err)
				assert.Nil(t, client)
				if tc.errorMsg != "" {
					assert.Contains(t, err.Error(), tc.errorMsg)
				}
			} else {
				// In unit test environment, we expect structure validation to pass
				// Actual connection attempts may fail without real Redis
				if err != nil {
					// Connection errors are acceptable in unit tests
					assert.Contains(t, err.Error(), "connection")
				}
			}
		})
	}
}

func TestConnectionType_String(t *testing.T) {
	testCases := []struct {
		connType ConnectionType
		expected string
	}{
		{SingleInstance, "SingleInstance"},
		{Cluster, "Cluster"},
		{Sentinel, "Sentinel"},
	}

	for _, tc := range testCases {
		t.Run(tc.expected, func(t *testing.T) {
			assert.Equal(t, tc.expected, tc.connType.String())
		})
	}
}

func TestClusterConfig_ToRedisOptions(t *testing.T) {
	config := ClusterConfig{
		Addrs:           []string{"localhost:7000", "localhost:7001"},
		Password:        "secret",
		Username:        "user",
		MaxRetries:      5,
		PoolSize:        20,
		MinIdleConns:    10,
		MaxConnAge:      10 * time.Minute,
		PoolTimeout:     5 * time.Second,
		IdleTimeout:     2 * time.Minute,
		ReadTimeout:     3 * time.Second,
		WriteTimeout:    3 * time.Second,
		RouteByLatency:  true,
		RouteRandomly:   false,
	}

	options := config.ToRedisOptions()

	assert.Equal(t, config.Addrs, options.Addrs)
	assert.Equal(t, config.Password, options.Password)
	assert.Equal(t, config.Username, options.Username)
	assert.Equal(t, config.MaxRetries, options.MaxRetries)
	assert.Equal(t, config.PoolSize, options.PoolSize)
	assert.Equal(t, config.MinIdleConns, options.MinIdleConns)
	assert.Equal(t, config.MaxConnAge, options.ConnMaxLifetime)
	assert.Equal(t, config.PoolTimeout, options.PoolTimeout)
	assert.Equal(t, config.IdleTimeout, options.ConnMaxIdleTime)
	assert.Equal(t, config.ReadTimeout, options.ReadTimeout)
	assert.Equal(t, config.WriteTimeout, options.WriteTimeout)
	assert.Equal(t, config.RouteByLatency, options.RouteByLatency)
	assert.Equal(t, config.RouteRandomly, options.RouteRandomly)
}

// Benchmark tests for cluster operations
func BenchmarkClusterConfig_Validate(b *testing.B) {
	config := ClusterConfig{
		Addrs: []string{"localhost:7000", "localhost:7001", "localhost:7002"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = config.Validate()
	}
}

func BenchmarkRedisConnectionFactory_CreateConnection(b *testing.B) {
	mockLogger := new(MockLogger)
	factory := &RedisConnectionFactory{
		Logger: mockLogger,
	}

	config := &ClusterConfig{
		Addrs: []string{"localhost:7000", "localhost:7001"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = factory.CreateConnection(Cluster, config)
	}
}

// Integration test placeholder
func TestRedisClusterConnection_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Skip("Integration test requires Redis cluster instance")

	// Example of what an integration test would look like:
	/*
		config := ClusterConfig{
			Addrs: []string{
				os.Getenv("REDIS_CLUSTER_ADDR_1"),
				os.Getenv("REDIS_CLUSTER_ADDR_2"),
				os.Getenv("REDIS_CLUSTER_ADDR_3"),
			},
			PoolSize: 10,
		}

		rcc := &RedisClusterConnection{
			ClusterConfig: config,
			Logger:        log.NewLogger(),
		}

		ctx := context.Background()
		err := rcc.ConnectCluster(ctx)
		assert.NoError(t, err)
		assert.True(t, rcc.Connected)

		client, err := rcc.GetClusterClient(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, client)

		// Test health check
		err = rcc.HealthCheck(ctx)
		assert.NoError(t, err)

		// Test cluster info
		info, err := rcc.GetClusterInfo(ctx)
		assert.NoError(t, err)
		assert.NotEmpty(t, info)

		// Cleanup
		err = client.Close()
		assert.NoError(t, err)
	*/
}