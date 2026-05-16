//go:build unit

package redis

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -------------------------------------------------------------------
// NewTenantPubSubRedisClient — invalid host causes ping failure
// -------------------------------------------------------------------

func TestNewTenantPubSubRedisClient_PingFails(t *testing.T) {
	t.Parallel()

	cfg := TenantPubSubRedisConfig{
		Host: "unreachable-redis-host-99999",
		Port: "6379",
	}

	ctx := context.Background()
	_, err := NewTenantPubSubRedisClient(ctx, cfg)
	require.Error(t, err, "should fail because the host is unreachable")
	assert.Contains(t, err.Error(), "ping failed")
}

// -------------------------------------------------------------------
// NewTenantPubSubRedisClient — BuildOptions error (empty host)
// -------------------------------------------------------------------

func TestNewTenantPubSubRedisClient_EmptyHost(t *testing.T) {
	t.Parallel()

	cfg := TenantPubSubRedisConfig{
		Host: "",
		Port: "6379",
	}

	ctx := context.Background()
	// BuildOptions may succeed with empty host (defaults to localhost), but ping will fail
	_, err := NewTenantPubSubRedisClient(ctx, cfg)
	// Either BuildOptions errors or ping fails — both result in an error
	require.Error(t, err)
}
