package poolmanager

import (
	"context"
	"fmt"
	"strings"
	"time"

	libLog "github.com/LerianStudio/lib-commons/v2/commons/log"
	libRedis "github.com/LerianStudio/lib-commons/v2/commons/redis"
	"github.com/redis/go-redis/v9"
)

// TenantValkeyClient defines the interface for a tenant-aware Valkey/Redis client.
// All keys are automatically prefixed with the tenant identifier using the pattern: tenant:{id}#
// This ensures complete tenant isolation at the key level.
type TenantValkeyClient interface {
	// Get retrieves the value of a key within the tenant's namespace.
	// Returns redis.Nil error if the key does not exist.
	Get(ctx context.Context, key string) (string, error)

	// Set stores a value for a key within the tenant's namespace.
	// If ttl is 0, the key has no expiration.
	Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error

	// Del removes one or more keys from the tenant's namespace.
	// Returns the number of keys that were removed.
	Del(ctx context.Context, keys ...string) (int64, error)

	// Keys returns all keys matching the pattern within the tenant's namespace.
	// The pattern is automatically prefixed with the tenant prefix.
	// Returned keys have the tenant prefix stripped.
	Keys(ctx context.Context, pattern string) ([]string, error)

	// Exists returns the number of keys that exist within the tenant's namespace.
	Exists(ctx context.Context, keys ...string) (int64, error)

	// GetTenantID returns the tenant ID for this client.
	GetTenantID() string

	// GetPrefix returns the key prefix used by this client (format: "tenant:{id}#").
	GetPrefix() string

	// GetUnderlyingClient returns the underlying Redis client for advanced operations.
	// Use with caution: operations on this client bypass tenant isolation.
	GetUnderlyingClient() redis.Cmdable
}

// tenantValkeyClient implements TenantValkeyClient.
type tenantValkeyClient struct {
	client   redis.Cmdable
	tenantID string
	prefix   string
	logger   libLog.Logger
}

// NewTenantValkeyClient creates a new tenant-aware Valkey/Redis client.
// Returns nil if client is nil or tenantID is empty.
//
// The client wraps common Redis operations and automatically prefixes all keys
// with "tenant:{tenantID}#" to ensure tenant isolation.
//
// Example:
//
//	client := NewTenantValkeyClient(redisClient, "org-123")
//	client.Set(ctx, "user:1", "data", 0)
//	// Stored as: tenant:org-123#user:1
func NewTenantValkeyClient(client redis.Cmdable, tenantID string) TenantValkeyClient {
	if client == nil {
		return nil
	}

	if tenantID == "" {
		return nil
	}

	return &tenantValkeyClient{
		client:   client,
		tenantID: tenantID,
		prefix:   fmt.Sprintf("tenant:%s#", tenantID),
	}
}

// NewTenantValkeyClientWithLogger creates a new tenant-aware Valkey/Redis client with logging support.
// Returns nil if client is nil or tenantID is empty.
//
// Example:
//
//	client := NewTenantValkeyClientWithLogger(redisClient, "org-123", logger)
//	client.Set(ctx, "user:1", "data", 0)
//	// Stored as: tenant:org-123#user:1
func NewTenantValkeyClientWithLogger(client redis.Cmdable, tenantID string, logger libLog.Logger) TenantValkeyClient {
	if client == nil {
		return nil
	}

	if tenantID == "" {
		return nil
	}

	return &tenantValkeyClient{
		client:   client,
		tenantID: tenantID,
		prefix:   fmt.Sprintf("tenant:%s#", tenantID),
		logger:   logger,
	}
}

// Get retrieves the value of a key within the tenant's namespace.
func (c *tenantValkeyClient) Get(ctx context.Context, key string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("context cannot be nil")
	}

	prefixedKey := c.prefixKey(key)

	result, err := c.client.Get(ctx, prefixedKey).Result()
	if err != nil {
		if c.logger != nil && err != redis.Nil {
			c.logger.Warnf("Valkey Get failed for tenant %s key %s: %v", c.tenantID, key, err)
		}
		return "", err
	}

	if c.logger != nil {
		c.logger.Infof("Valkey Get for tenant %s key %s", c.tenantID, key)
	}

	return result, nil
}

// Set stores a value for a key within the tenant's namespace.
func (c *tenantValkeyClient) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	if ctx == nil {
		return fmt.Errorf("context cannot be nil")
	}

	prefixedKey := c.prefixKey(key)

	err := c.client.Set(ctx, prefixedKey, value, ttl).Err()
	if err != nil {
		if c.logger != nil {
			c.logger.Errorf("Valkey Set failed for tenant %s key %s: %v", c.tenantID, key, err)
		}
		return err
	}

	if c.logger != nil {
		c.logger.Infof("Valkey Set for tenant %s key %s (ttl: %v)", c.tenantID, key, ttl)
	}

	return nil
}

// Del removes one or more keys from the tenant's namespace.
func (c *tenantValkeyClient) Del(ctx context.Context, keys ...string) (int64, error) {
	if ctx == nil {
		return 0, fmt.Errorf("context cannot be nil")
	}

	if len(keys) == 0 {
		return 0, nil
	}

	prefixedKeys := make([]string, len(keys))
	for i, key := range keys {
		prefixedKeys[i] = c.prefixKey(key)
	}

	result, err := c.client.Del(ctx, prefixedKeys...).Result()
	if err != nil {
		if c.logger != nil {
			c.logger.Errorf("Valkey Del failed for tenant %s keys %v: %v", c.tenantID, keys, err)
		}
		return 0, err
	}

	if c.logger != nil {
		c.logger.Infof("Valkey Del for tenant %s keys %v (deleted: %d)", c.tenantID, keys, result)
	}

	return result, nil
}

// Keys returns all keys matching the pattern within the tenant's namespace.
func (c *tenantValkeyClient) Keys(ctx context.Context, pattern string) ([]string, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context cannot be nil")
	}

	prefixedPattern := c.prefixKey(pattern)

	keys, err := c.client.Keys(ctx, prefixedPattern).Result()
	if err != nil {
		return nil, err
	}

	// Strip tenant prefix from returned keys
	result := make([]string, len(keys))
	for i, key := range keys {
		result[i] = c.stripPrefix(key)
	}

	return result, nil
}

// Exists returns the number of keys that exist within the tenant's namespace.
func (c *tenantValkeyClient) Exists(ctx context.Context, keys ...string) (int64, error) {
	if ctx == nil {
		return 0, fmt.Errorf("context cannot be nil")
	}

	if len(keys) == 0 {
		return 0, nil
	}

	prefixedKeys := make([]string, len(keys))
	for i, key := range keys {
		prefixedKeys[i] = c.prefixKey(key)
	}

	return c.client.Exists(ctx, prefixedKeys...).Result()
}

// GetTenantID returns the tenant ID for this client.
func (c *tenantValkeyClient) GetTenantID() string {
	return c.tenantID
}

// GetPrefix returns the key prefix used by this client.
func (c *tenantValkeyClient) GetPrefix() string {
	return c.prefix
}

// GetUnderlyingClient returns the underlying Redis client for advanced operations.
// Use with caution: operations on this client bypass tenant isolation.
func (c *tenantValkeyClient) GetUnderlyingClient() redis.Cmdable {
	return c.client
}

// NewTenantValkeyClientFromConnection creates a tenant-aware Valkey/Redis client
// from a RedisConnection. This is the preferred way to create a tenant client
// when using the commons/redis infrastructure.
//
// The function retrieves the underlying client from the RedisConnection and wraps
// it with tenant-aware key prefixing.
//
// Example:
//
//	conn := &redis.RedisConnection{...}
//	if err := conn.Connect(ctx); err != nil {
//	    return err
//	}
//	client, err := NewTenantValkeyClientFromConnection(ctx, conn, "org-123")
//	if err != nil {
//	    return err
//	}
//	client.Set(ctx, "user:1", "data", 0)
//	// Stored as: tenant:org-123#user:1
func NewTenantValkeyClientFromConnection(ctx context.Context, conn *libRedis.RedisConnection, tenantID string) (TenantValkeyClient, error) {
	if conn == nil {
		return nil, fmt.Errorf("redis connection is required")
	}

	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	client, err := conn.GetClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get redis client: %w", err)
	}

	return NewTenantValkeyClient(client, tenantID), nil
}

// prefixKey adds the tenant prefix to a key.
func (c *tenantValkeyClient) prefixKey(key string) string {
	return c.prefix + key
}

// stripPrefix removes the tenant prefix from a key.
func (c *tenantValkeyClient) stripPrefix(key string) string {
	return strings.TrimPrefix(key, c.prefix)
}
