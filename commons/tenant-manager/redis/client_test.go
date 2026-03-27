package redis

import (
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildOptions_DefaultPort(t *testing.T) {
	t.Parallel()

	cfg := TenantPubSubRedisConfig{
		Host: "localhost",
	}

	opts, err := BuildOptions(cfg)

	require.NoError(t, err)
	assert.Equal(t, "localhost:6379", opts.Addr)
}

func TestBuildOptions_CustomPort(t *testing.T) {
	t.Parallel()

	cfg := TenantPubSubRedisConfig{
		Host: "redis.example.com",
		Port: "6380",
	}

	opts, err := BuildOptions(cfg)

	require.NoError(t, err)
	assert.Equal(t, "redis.example.com:6380", opts.Addr)
}

func TestBuildOptions_WithPassword(t *testing.T) {
	t.Parallel()

	cfg := TenantPubSubRedisConfig{
		Host:     "localhost",
		Password: "s3cret",
	}

	opts, err := BuildOptions(cfg)

	require.NoError(t, err)
	assert.Equal(t, "s3cret", opts.Password)
}

func TestBuildOptions_WithoutPassword(t *testing.T) {
	t.Parallel()

	cfg := TenantPubSubRedisConfig{
		Host: "localhost",
	}

	opts, err := BuildOptions(cfg)

	require.NoError(t, err)
	assert.Empty(t, opts.Password)
}

func TestBuildOptions_WithTLS(t *testing.T) {
	t.Parallel()

	cfg := TenantPubSubRedisConfig{
		Host: "redis.example.com",
		TLS:  true,
	}

	opts, err := BuildOptions(cfg)

	require.NoError(t, err)
	require.NotNil(t, opts.TLSConfig)
	assert.Equal(t, uint16(tls.VersionTLS12), opts.TLSConfig.MinVersion)
}

func TestBuildOptions_WithoutTLS(t *testing.T) {
	t.Parallel()

	cfg := TenantPubSubRedisConfig{
		Host: "localhost",
	}

	opts, err := BuildOptions(cfg)

	require.NoError(t, err)
	assert.Nil(t, opts.TLSConfig)
}

func TestBuildOptions_EmptyHost(t *testing.T) {
	t.Parallel()

	cfg := TenantPubSubRedisConfig{}

	_, err := BuildOptions(cfg)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "host")
}

func TestBuildOptions_AllFieldsSet(t *testing.T) {
	t.Parallel()

	cfg := TenantPubSubRedisConfig{
		Host:     "redis.prod.internal",
		Port:     "6380",
		Password: "secret-pass",
		TLS:      true,
	}

	opts, err := BuildOptions(cfg)

	require.NoError(t, err)
	assert.Equal(t, "redis.prod.internal:6380", opts.Addr)
	assert.Equal(t, "secret-pass", opts.Password)
	require.NotNil(t, opts.TLSConfig)
	assert.Equal(t, uint16(tls.VersionTLS12), opts.TLSConfig.MinVersion)
}
