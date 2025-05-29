package redis

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestSmartRedisConnection_BackwardCompatibility tests that existing client code continues to work
func TestSmartRedisConnection_BackwardCompatibility(t *testing.T) {
	t.Run("existing single instance pattern works", func(t *testing.T) {
		// This exact pattern should continue to work unchanged
		rc := &RedisConnection{
			Addr:     "localhost:6379",
			Password: "password",
			DB:       0,
			Logger:   nil, // Use nil logger for simplicity
		}

		ctx := context.Background()
		err := rc.Connect(ctx)
		
		// Connection will fail without real Redis, but the API should work
		// The important thing is the method signature and structure hasn't changed
		assert.Error(t, err) // Expected since no real Redis instance
		assert.NotNil(t, rc.autoDetector)
		assert.NotNil(t, rc.detectedConfig)
	})

	t.Run("GetClient method signature unchanged", func(t *testing.T) {
		rc := &RedisConnection{
			Addr:   "localhost:6379",
			Logger: nil,
		}

		ctx := context.Background()
		
		// This exact pattern should continue to work unchanged
		client, err := rc.GetClient(ctx)
		
		// Even if connection fails, the method signature is correct
		assert.Error(t, err) // Expected since no real Redis instance
		assert.Nil(t, client)
	})
}

// TestAddressParsing tests the address parsing logic
func TestAddressParsing(t *testing.T) {
	testCases := []struct {
		name     string
		addr     string
		expected []string
	}{
		{
			name:     "single address",
			addr:     "localhost:6379",
			expected: []string{"localhost:6379"},
		},
		{
			name:     "multiple addresses",
			addr:     "host1:6379,host2:6380,host3:6381",
			expected: []string{"host1:6379", "host2:6380", "host3:6381"},
		},
		{
			name:     "addresses with spaces",
			addr:     "host1:6379, host2:6380 , host3:6381",
			expected: []string{"host1:6379", "host2:6380", "host3:6381"},
		},
		{
			name:     "empty address defaults",
			addr:     "",
			expected: []string{"localhost:6379"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rc := &RedisConnection{Addr: tc.addr}
			addresses := rc.parseAddresses()
			assert.Equal(t, tc.expected, addresses)
		})
	}
}

// TestConnectionAPI_Compatibility tests that all existing API methods work
func TestConnectionAPI_Compatibility(t *testing.T) {
	t.Run("all RedisClient interface methods work", func(t *testing.T) {
		rc := &RedisConnection{
			Addr: "localhost:6379",
		}

		ctx := context.Background()

		// These method calls should not panic even without connection
		pingCmd := rc.Ping(ctx)
		assert.NotNil(t, pingCmd)

		setCmd := rc.Set(ctx, "key", "value", time.Minute)
		assert.NotNil(t, setCmd)

		getCmd := rc.Get(ctx, "key")
		assert.NotNil(t, getCmd)

		delCmd := rc.Del(ctx, "key")
		assert.NotNil(t, delCmd)

		err := rc.Close()
		assert.NoError(t, err)
	})
}

// TestGCPDetection_EnvironmentVariables tests GCP environment detection
func TestGCPDetection_EnvironmentVariables(t *testing.T) {
	t.Run("non-GCP environment", func(t *testing.T) {
		// Clear GCP environment variables
		gcpEnvVars := []string{
			"GOOGLE_APPLICATION_CREDENTIALS", "GCP_PROJECT_ID", "GOOGLE_CLOUD_PROJECT",
			"GAE_APPLICATION", "GAE_SERVICE", "K_SERVICE", "FUNCTION_NAME", "GCP_VALKEY_AUTH",
		}
		for _, env := range gcpEnvVars {
			_ = os.Unsetenv(env)
		}

		rc := &RedisConnection{
			Addr:   "localhost:6379",
			Logger: nil,
		}

		ctx := context.Background()
		err := rc.Connect(ctx)

		assert.Error(t, err) // Connection will fail without real Redis
		assert.NotNil(t, rc.detectedConfig)
		assert.False(t, rc.IsGCPAuthenticated())
		assert.Equal(t, "standard", rc.getAuthType())
	})
}

// Note: mockAny variable was removed as it was unused
// If you need a mock helper in the future, you can add it back