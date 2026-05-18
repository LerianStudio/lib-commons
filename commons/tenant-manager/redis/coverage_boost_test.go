//go:build unit

package redis

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -------------------------------------------------------------------
// NewTenantPubSubRedisClient — invalid host causes ping failure
// -------------------------------------------------------------------

func TestNewTenantPubSubRedisClient_PingFails(t *testing.T) {
	t.Parallel()

	cfg := TenantPubSubRedisConfig{
		Host: "127.0.0.1",
		Port: "0",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := NewTenantPubSubRedisClient(ctx, cfg)
	require.Error(t, err, "should fail because the host is unreachable")
	assert.Error(t, err)
}

// -------------------------------------------------------------------
// NewTenantPubSubRedisClient — BuildOptions error (empty host)
// -------------------------------------------------------------------

func TestNewTenantPubSubRedisClient_EmptyHost(t *testing.T) {
	t.Parallel()

	cfg := TenantPubSubRedisConfig{
		Host: "",
		Port: "0",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	// BuildOptions may succeed with empty host (defaults to localhost), but ping will fail
	_, err := NewTenantPubSubRedisClient(ctx, cfg)
	// Either BuildOptions errors or ping fails — both result in an error
	require.Error(t, err)
}
