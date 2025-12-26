package poolmanager

import (
	"context"
	"testing"
	"time"

	libRedis "github.com/LerianStudio/lib-commons/v2/commons/redis"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLogger is defined in pool_manager_pg_test.go and reused here

func TestTenantValkeyClient_NewTenantValkeyClient(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	tests := []struct {
		name     string
		client   redis.Cmdable
		tenantID string
		wantNil  bool
	}{
		{
			name:     "valid client and tenant ID",
			client:   client,
			tenantID: "tenant-123",
			wantNil:  false,
		},
		{
			name:     "valid client with UUID tenant ID",
			client:   client,
			tenantID: "550e8400-e29b-41d4-a716-446655440000",
			wantNil:  false,
		},
		{
			name:     "nil client returns nil",
			client:   nil,
			tenantID: "tenant-123",
			wantNil:  true,
		},
		{
			name:     "empty tenant ID returns nil",
			client:   client,
			tenantID: "",
			wantNil:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NewTenantValkeyClient(tt.client, tt.tenantID)
			if tt.wantNil {
				assert.Nil(t, result, "expected nil TenantValkeyClient")
			} else {
				assert.NotNil(t, result, "expected non-nil TenantValkeyClient")
			}
		})
	}
}

func TestTenantValkeyClient_KeyPrefixing(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	tenantID := "tenant-abc"
	tenantClient := NewTenantValkeyClient(client, tenantID)
	require.NotNil(t, tenantClient)

	ctx := context.Background()

	// Set a value using the tenant client
	err = tenantClient.Set(ctx, "mykey", "myvalue", 0)
	require.NoError(t, err)

	// Verify the key is stored with tenant prefix in raw client
	expectedKey := "tenant:tenant-abc#mykey"
	val, err := client.Get(ctx, expectedKey).Result()
	require.NoError(t, err)
	assert.Equal(t, "myvalue", val, "value should be stored with tenant prefix")

	// Verify the key without prefix does NOT exist
	_, err = client.Get(ctx, "mykey").Result()
	assert.Equal(t, redis.Nil, err, "key without prefix should not exist")
}

func TestTenantValkeyClient_Get(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	tenantID := "tenant-get"
	tenantClient := NewTenantValkeyClient(client, tenantID)
	require.NotNil(t, tenantClient)

	ctx := context.Background()

	tests := []struct {
		name        string
		setup       func()
		key         string
		expected    string
		expectError bool
	}{
		{
			name: "get existing key",
			setup: func() {
				client.Set(ctx, "tenant:tenant-get#existing", "value123", 0)
			},
			key:         "existing",
			expected:    "value123",
			expectError: false,
		},
		{
			name:        "get non-existing key returns error",
			setup:       func() {},
			key:         "nonexistent",
			expected:    "",
			expectError: true,
		},
		{
			name: "get does not access other tenant's data",
			setup: func() {
				client.Set(ctx, "tenant:other-tenant#sharedkey", "othervalue", 0)
			},
			key:         "sharedkey",
			expected:    "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mr.FlushAll()
			tt.setup()

			val, err := tenantClient.Get(ctx, tt.key)
			if tt.expectError {
				assert.Error(t, err, "expected an error")
			} else {
				assert.NoError(t, err, "expected no error")
				assert.Equal(t, tt.expected, val, "value should match")
			}
		})
	}
}

func TestTenantValkeyClient_Set(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	tenantID := "tenant-set"
	tenantClient := NewTenantValkeyClient(client, tenantID)
	require.NotNil(t, tenantClient)

	ctx := context.Background()

	tests := []struct {
		name  string
		key   string
		value interface{}
		ttl   time.Duration
	}{
		{
			name:  "set string value",
			key:   "string-key",
			value: "string-value",
			ttl:   0,
		},
		{
			name:  "set integer value",
			key:   "int-key",
			value: 42,
			ttl:   0,
		},
		{
			name:  "set value with TTL",
			key:   "ttl-key",
			value: "expires-soon",
			ttl:   time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mr.FlushAll()

			err := tenantClient.Set(ctx, tt.key, tt.value, tt.ttl)
			require.NoError(t, err, "set should not error")

			// Verify value is stored with prefix
			prefixedKey := "tenant:" + tenantID + "#" + tt.key
			exists := client.Exists(ctx, prefixedKey).Val()
			assert.Equal(t, int64(1), exists, "key should exist with tenant prefix")

			// Verify TTL if set
			if tt.ttl > 0 {
				ttl := client.TTL(ctx, prefixedKey).Val()
				assert.True(t, ttl > 0, "TTL should be set")
			}
		})
	}
}

func TestTenantValkeyClient_Del(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	tenantID := "tenant-del"
	tenantClient := NewTenantValkeyClient(client, tenantID)
	require.NotNil(t, tenantClient)

	ctx := context.Background()

	tests := []struct {
		name     string
		setup    func()
		keys     []string
		expected int64
	}{
		{
			name: "delete single existing key",
			setup: func() {
				client.Set(ctx, "tenant:tenant-del#key1", "value1", 0)
			},
			keys:     []string{"key1"},
			expected: 1,
		},
		{
			name: "delete multiple existing keys",
			setup: func() {
				client.Set(ctx, "tenant:tenant-del#key1", "value1", 0)
				client.Set(ctx, "tenant:tenant-del#key2", "value2", 0)
				client.Set(ctx, "tenant:tenant-del#key3", "value3", 0)
			},
			keys:     []string{"key1", "key2", "key3"},
			expected: 3,
		},
		{
			name:     "delete non-existing key returns 0",
			setup:    func() {},
			keys:     []string{"nonexistent"},
			expected: 0,
		},
		{
			name: "delete does not affect other tenant's keys",
			setup: func() {
				client.Set(ctx, "tenant:other-tenant#shared", "othervalue", 0)
			},
			keys:     []string{"shared"},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mr.FlushAll()
			tt.setup()

			count, err := tenantClient.Del(ctx, tt.keys...)
			require.NoError(t, err, "del should not error")
			assert.Equal(t, tt.expected, count, "deleted count should match")
		})
	}
}

func TestTenantValkeyClient_Keys(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	tenantID := "tenant-keys"
	tenantClient := NewTenantValkeyClient(client, tenantID)
	require.NotNil(t, tenantClient)

	ctx := context.Background()

	tests := []struct {
		name        string
		setup       func()
		pattern     string
		expected    []string
		expectedLen int
	}{
		{
			name: "keys with wildcard pattern",
			setup: func() {
				client.Set(ctx, "tenant:tenant-keys#user:1", "val1", 0)
				client.Set(ctx, "tenant:tenant-keys#user:2", "val2", 0)
				client.Set(ctx, "tenant:tenant-keys#order:1", "val3", 0)
			},
			pattern:     "user:*",
			expectedLen: 2,
		},
		{
			name: "keys with exact pattern",
			setup: func() {
				client.Set(ctx, "tenant:tenant-keys#exact", "val1", 0)
			},
			pattern:     "exact",
			expectedLen: 1,
		},
		{
			name: "keys does not return other tenant's keys",
			setup: func() {
				client.Set(ctx, "tenant:other-tenant#user:1", "val1", 0)
				client.Set(ctx, "tenant:tenant-keys#user:2", "val2", 0)
			},
			pattern:     "user:*",
			expectedLen: 1,
		},
		{
			name:        "keys returns empty for no matches",
			setup:       func() {},
			pattern:     "nonexistent*",
			expectedLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mr.FlushAll()
			tt.setup()

			keys, err := tenantClient.Keys(ctx, tt.pattern)
			require.NoError(t, err, "keys should not error")
			assert.Len(t, keys, tt.expectedLen, "keys count should match")

			// Verify returned keys do NOT have tenant prefix (stripped)
			for _, key := range keys {
				assert.NotContains(t, key, "tenant:", "returned keys should not have tenant prefix")
			}
		})
	}
}

func TestTenantValkeyClient_Exists(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	tenantID := "tenant-exists"
	tenantClient := NewTenantValkeyClient(client, tenantID)
	require.NotNil(t, tenantClient)

	ctx := context.Background()

	tests := []struct {
		name     string
		setup    func()
		keys     []string
		expected int64
	}{
		{
			name: "exists single key",
			setup: func() {
				client.Set(ctx, "tenant:tenant-exists#key1", "val1", 0)
			},
			keys:     []string{"key1"},
			expected: 1,
		},
		{
			name: "exists multiple keys - all exist",
			setup: func() {
				client.Set(ctx, "tenant:tenant-exists#key1", "val1", 0)
				client.Set(ctx, "tenant:tenant-exists#key2", "val2", 0)
			},
			keys:     []string{"key1", "key2"},
			expected: 2,
		},
		{
			name: "exists multiple keys - some exist",
			setup: func() {
				client.Set(ctx, "tenant:tenant-exists#key1", "val1", 0)
			},
			keys:     []string{"key1", "key2", "key3"},
			expected: 1,
		},
		{
			name:     "exists non-existing keys returns 0",
			setup:    func() {},
			keys:     []string{"nonexistent1", "nonexistent2"},
			expected: 0,
		},
		{
			name: "exists does not count other tenant's keys",
			setup: func() {
				client.Set(ctx, "tenant:other-tenant#shared", "val1", 0)
			},
			keys:     []string{"shared"},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mr.FlushAll()
			tt.setup()

			count, err := tenantClient.Exists(ctx, tt.keys...)
			require.NoError(t, err, "exists should not error")
			assert.Equal(t, tt.expected, count, "exists count should match")
		})
	}
}

func TestTenantValkeyClient_TenantIsolation(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	ctx := context.Background()

	tenant1Client := NewTenantValkeyClient(client, "tenant-1")
	tenant2Client := NewTenantValkeyClient(client, "tenant-2")

	require.NotNil(t, tenant1Client)
	require.NotNil(t, tenant2Client)

	// Set same key for both tenants
	err = tenant1Client.Set(ctx, "shared-key", "tenant-1-value", 0)
	require.NoError(t, err)

	err = tenant2Client.Set(ctx, "shared-key", "tenant-2-value", 0)
	require.NoError(t, err)

	// Verify each tenant sees their own value
	val1, err := tenant1Client.Get(ctx, "shared-key")
	require.NoError(t, err)
	assert.Equal(t, "tenant-1-value", val1, "tenant 1 should see their own value")

	val2, err := tenant2Client.Get(ctx, "shared-key")
	require.NoError(t, err)
	assert.Equal(t, "tenant-2-value", val2, "tenant 2 should see their own value")

	// Delete from tenant 1 should not affect tenant 2
	count, err := tenant1Client.Del(ctx, "shared-key")
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// Tenant 1 should not find the key
	_, err = tenant1Client.Get(ctx, "shared-key")
	assert.Error(t, err, "tenant 1 key should be deleted")

	// Tenant 2 should still have their key
	val2, err = tenant2Client.Get(ctx, "shared-key")
	require.NoError(t, err)
	assert.Equal(t, "tenant-2-value", val2, "tenant 2 key should still exist")
}

func TestTenantValkeyClient_NilContext(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	tenantClient := NewTenantValkeyClient(client, "tenant-ctx")
	require.NotNil(t, tenantClient)

	// All methods should handle nil context gracefully (Redis will error)
	// This ensures we propagate errors correctly

	//nolint:staticcheck // SA1012: Testing nil context handling intentionally
	_, err = tenantClient.Get(nil, "key")
	assert.Error(t, err, "get with nil context should error")

	//nolint:staticcheck // SA1012: Testing nil context handling intentionally
	err = tenantClient.Set(nil, "key", "value", 0)
	assert.Error(t, err, "set with nil context should error")
}

func TestTenantValkeyClient_GetTenantID(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	tests := []struct {
		name     string
		tenantID string
	}{
		{
			name:     "simple tenant ID",
			tenantID: "tenant-123",
		},
		{
			name:     "UUID tenant ID",
			tenantID: "550e8400-e29b-41d4-a716-446655440000",
		},
		{
			name:     "complex tenant ID",
			tenantID: "org-123-env-prod",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tenantClient := NewTenantValkeyClient(client, tt.tenantID)
			require.NotNil(t, tenantClient)

			assert.Equal(t, tt.tenantID, tenantClient.GetTenantID(), "tenant ID should match")
		})
	}
}

func TestTenantValkeyClient_GetPrefix(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	tests := []struct {
		name           string
		tenantID       string
		expectedPrefix string
	}{
		{
			name:           "simple tenant ID",
			tenantID:       "tenant-123",
			expectedPrefix: "tenant:tenant-123#",
		},
		{
			name:           "UUID tenant ID",
			tenantID:       "550e8400-e29b-41d4-a716-446655440000",
			expectedPrefix: "tenant:550e8400-e29b-41d4-a716-446655440000#",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tenantClient := NewTenantValkeyClient(client, tt.tenantID)
			require.NotNil(t, tenantClient)

			assert.Equal(t, tt.expectedPrefix, tenantClient.GetPrefix(), "prefix should match expected format")
		})
	}
}

func TestTenantValkeyClient_GetUnderlyingClient(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	tenantClient := NewTenantValkeyClient(client, "tenant-underlying")
	require.NotNil(t, tenantClient)

	underlyingClient := tenantClient.GetUnderlyingClient()
	assert.NotNil(t, underlyingClient, "underlying client should not be nil")

	// Verify we can use the underlying client directly (bypassing tenant isolation)
	ctx := context.Background()
	err = underlyingClient.Set(ctx, "direct-key", "direct-value", 0).Err()
	require.NoError(t, err, "should be able to use underlying client directly")

	val, err := underlyingClient.Get(ctx, "direct-key").Result()
	require.NoError(t, err)
	assert.Equal(t, "direct-value", val, "value should be retrievable via underlying client")

	// Verify the key is NOT prefixed (direct access)
	_, err = tenantClient.Get(ctx, "direct-key")
	assert.Error(t, err, "tenant client should not find unprefixed key")
}

func TestNewTenantValkeyClientFromConnection(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	ctx := context.Background()

	tests := []struct {
		name        string
		setupConn   func() *libRedis.RedisConnection
		tenantID    string
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid connection and tenant ID",
			setupConn: func() *libRedis.RedisConnection {
				conn := &libRedis.RedisConnection{
					Address: []string{mr.Addr()},
					Logger:  &mockLogger{},
				}
				return conn
			},
			tenantID:    "tenant-from-conn",
			expectError: false,
		},
		{
			name: "nil connection returns error",
			setupConn: func() *libRedis.RedisConnection {
				return nil
			},
			tenantID:    "tenant-123",
			expectError: true,
			errorMsg:    "redis connection is required",
		},
		{
			name: "empty tenant ID returns error",
			setupConn: func() *libRedis.RedisConnection {
				conn := &libRedis.RedisConnection{
					Address: []string{mr.Addr()},
					Logger:  &mockLogger{},
				}
				return conn
			},
			tenantID:    "",
			expectError: true,
			errorMsg:    "tenant ID is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := tt.setupConn()

			client, err := NewTenantValkeyClientFromConnection(ctx, conn, tt.tenantID)

			if tt.expectError {
				assert.Error(t, err, "expected an error")
				assert.Nil(t, client, "client should be nil on error")
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg, "error message should match")
				}
			} else {
				assert.NoError(t, err, "expected no error")
				assert.NotNil(t, client, "client should not be nil")

				// Verify the client works correctly
				err = client.Set(ctx, "test-key", "test-value", 0)
				require.NoError(t, err)

				val, err := client.Get(ctx, "test-key")
				require.NoError(t, err)
				assert.Equal(t, "test-value", val)

				// Verify tenant ID and prefix are set correctly
				assert.Equal(t, tt.tenantID, client.GetTenantID())
				assert.Equal(t, "tenant:"+tt.tenantID+"#", client.GetPrefix())
			}
		})
	}
}

// TestTenantValkeyClient_Del_EmptyKeys tests Del with empty keys.
func TestTenantValkeyClient_Del_EmptyKeys(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	tenantClient := NewTenantValkeyClient(client, "tenant-del-empty")
	require.NotNil(t, tenantClient)

	ctx := context.Background()

	// Del with no keys should return 0, no error
	count, err := tenantClient.Del(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

// TestTenantValkeyClient_Exists_EmptyKeys tests Exists with empty keys.
func TestTenantValkeyClient_Exists_EmptyKeys(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	tenantClient := NewTenantValkeyClient(client, "tenant-exists-empty")
	require.NotNil(t, tenantClient)

	ctx := context.Background()

	// Exists with no keys should return 0, no error
	count, err := tenantClient.Exists(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

// TestTenantValkeyClient_Del_NilContext tests Del with nil context.
func TestTenantValkeyClient_Del_NilContext(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	tenantClient := NewTenantValkeyClient(client, "tenant-del-nil")
	require.NotNil(t, tenantClient)

	//nolint:staticcheck // SA1012: Testing nil context handling intentionally
	_, err = tenantClient.Del(nil, "key1")
	assert.Error(t, err, "del with nil context should error")
}

// TestTenantValkeyClient_Keys_NilContext tests Keys with nil context.
func TestTenantValkeyClient_Keys_NilContext(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	tenantClient := NewTenantValkeyClient(client, "tenant-keys-nil")
	require.NotNil(t, tenantClient)

	//nolint:staticcheck // SA1012: Testing nil context handling intentionally
	_, err = tenantClient.Keys(nil, "*")
	assert.Error(t, err, "keys with nil context should error")
}

// TestTenantValkeyClient_Exists_NilContext tests Exists with nil context.
func TestTenantValkeyClient_Exists_NilContext(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	tenantClient := NewTenantValkeyClient(client, "tenant-exists-nil")
	require.NotNil(t, tenantClient)

	//nolint:staticcheck // SA1012: Testing nil context handling intentionally
	_, err = tenantClient.Exists(nil, "key1")
	assert.Error(t, err, "exists with nil context should error")
}

func TestNewTenantValkeyClientFromConnection_Integration(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	ctx := context.Background()

	// Create a RedisConnection and connect
	conn := &libRedis.RedisConnection{
		Address: []string{mr.Addr()},
		Logger:  &mockLogger{},
	}

	err = conn.Connect(ctx)
	require.NoError(t, err, "connection should succeed")
	defer conn.Close()

	// Create tenant clients from the same connection
	tenant1Client, err := NewTenantValkeyClientFromConnection(ctx, conn, "tenant-1")
	require.NoError(t, err)
	require.NotNil(t, tenant1Client)

	tenant2Client, err := NewTenantValkeyClientFromConnection(ctx, conn, "tenant-2")
	require.NoError(t, err)
	require.NotNil(t, tenant2Client)

	// Verify tenant isolation with clients created from same connection
	err = tenant1Client.Set(ctx, "shared-key", "tenant-1-value", 0)
	require.NoError(t, err)

	err = tenant2Client.Set(ctx, "shared-key", "tenant-2-value", 0)
	require.NoError(t, err)

	val1, err := tenant1Client.Get(ctx, "shared-key")
	require.NoError(t, err)
	assert.Equal(t, "tenant-1-value", val1, "tenant 1 should see their own value")

	val2, err := tenant2Client.Get(ctx, "shared-key")
	require.NoError(t, err)
	assert.Equal(t, "tenant-2-value", val2, "tenant 2 should see their own value")

	// Verify underlying client access works
	underlyingClient := tenant1Client.GetUnderlyingClient()
	assert.NotNil(t, underlyingClient)

	// Verify both clients share the same underlying connection
	assert.Equal(t, tenant1Client.GetUnderlyingClient(), tenant2Client.GetUnderlyingClient(),
		"both tenant clients should share the same underlying client")
}
