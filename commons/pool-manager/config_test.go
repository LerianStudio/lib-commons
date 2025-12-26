package poolmanager

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	tests := []struct {
		name      string
		checkFunc func(t *testing.T, cfg *Config)
	}{
		{
			name: "Enabled should default to true",
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.True(t, cfg.Enabled, "Enabled should default to true")
			},
		},
		{
			name: "ApplicationName should be empty by default",
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.Empty(t, cfg.ApplicationName, "ApplicationName should be empty by default")
			},
		},
		{
			name: "PoolManagerURL should be empty by default",
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.Empty(t, cfg.PoolManagerURL, "PoolManagerURL should be empty by default")
			},
		},
		{
			name: "CacheTTL should default to 24 hours",
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 24*time.Hour, cfg.CacheTTL, "CacheTTL should default to 24 hours")
			},
		},
		{
			name: "TenantClaimKey should default to 'tenantId'",
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "tenantId", cfg.TenantClaimKey, "TenantClaimKey should default to 'tenantId'")
			},
		},
		{
			name: "Pool.MaxSize should default to 100",
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 100, cfg.Pool.MaxSize, "Pool.MaxSize should default to 100")
			},
		},
		{
			name: "Pool.IdleTimeout should default to 30 minutes",
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 30*time.Minute, cfg.Pool.IdleTimeout, "Pool.IdleTimeout should default to 30 minutes")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			require.NotNil(t, cfg, "DefaultConfig should not return nil")
			tt.checkFunc(t, cfg)
		})
	}
}

func TestConfigFromEnv(t *testing.T) {
	// Helper to clear environment variables after each test
	clearEnv := func() {
		os.Unsetenv("TENANT_ENABLED")
		os.Unsetenv("TENANT_APPLICATION_NAME")
		os.Unsetenv("POOL_MANAGER_URL")
		os.Unsetenv("TENANT_CACHE_TTL")
		os.Unsetenv("TENANT_CLAIM_KEY")
		os.Unsetenv("TENANT_POOL_MAX_SIZE")
		os.Unsetenv("TENANT_POOL_IDLE_TIMEOUT")
	}

	tests := []struct {
		name      string
		envVars   map[string]string
		checkFunc func(t *testing.T, cfg *Config)
	}{
		{
			name:    "Should use defaults when no env vars set",
			envVars: map[string]string{},
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.True(t, cfg.Enabled, "Enabled should default to true")
				assert.Equal(t, 24*time.Hour, cfg.CacheTTL, "CacheTTL should default to 24h")
				assert.Equal(t, "tenantId", cfg.TenantClaimKey, "TenantClaimKey should default to 'tenantId'")
				assert.Equal(t, 100, cfg.Pool.MaxSize, "Pool.MaxSize should default to 100")
				assert.Equal(t, 30*time.Minute, cfg.Pool.IdleTimeout, "Pool.IdleTimeout should default to 30m")
			},
		},
		{
			name: "Should load Enabled from env var",
			envVars: map[string]string{
				"TENANT_ENABLED": "false",
			},
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.False(t, cfg.Enabled, "Enabled should be false when env var is 'false'")
			},
		},
		{
			name: "Should load ApplicationName from env var",
			envVars: map[string]string{
				"TENANT_APPLICATION_NAME": "midaz",
			},
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "midaz", cfg.ApplicationName, "ApplicationName should be 'midaz'")
			},
		},
		{
			name: "Should load PoolManagerURL from env var",
			envVars: map[string]string{
				"POOL_MANAGER_URL": "http://pool-manager:8080",
			},
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "http://pool-manager:8080", cfg.PoolManagerURL)
			},
		},
		{
			name: "Should load CacheTTL from env var as duration string",
			envVars: map[string]string{
				"TENANT_CACHE_TTL": "1h",
			},
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 1*time.Hour, cfg.CacheTTL, "CacheTTL should be 1 hour")
			},
		},
		{
			name: "Should load TenantClaimKey from env var",
			envVars: map[string]string{
				"TENANT_CLAIM_KEY": "tenant_id",
			},
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "tenant_id", cfg.TenantClaimKey)
			},
		},
		{
			name: "Should load Pool.MaxSize from env var",
			envVars: map[string]string{
				"TENANT_POOL_MAX_SIZE": "200",
			},
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 200, cfg.Pool.MaxSize, "Pool.MaxSize should be 200")
			},
		},
		{
			name: "Should load Pool.IdleTimeout from env var",
			envVars: map[string]string{
				"TENANT_POOL_IDLE_TIMEOUT": "1h",
			},
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 1*time.Hour, cfg.Pool.IdleTimeout)
			},
		},
		{
			name: "Should load all env vars together",
			envVars: map[string]string{
				"TENANT_ENABLED":           "true",
				"TENANT_APPLICATION_NAME":  "plugin-fees",
				"POOL_MANAGER_URL":         "https://pool-manager.example.com",
				"TENANT_CACHE_TTL":         "12h",
				"TENANT_CLAIM_KEY":         "organization_id",
				"TENANT_POOL_MAX_SIZE":     "50",
				"TENANT_POOL_IDLE_TIMEOUT": "15m",
			},
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.True(t, cfg.Enabled)
				assert.Equal(t, "plugin-fees", cfg.ApplicationName)
				assert.Equal(t, "https://pool-manager.example.com", cfg.PoolManagerURL)
				assert.Equal(t, 12*time.Hour, cfg.CacheTTL)
				assert.Equal(t, "organization_id", cfg.TenantClaimKey)
				assert.Equal(t, 50, cfg.Pool.MaxSize)
				assert.Equal(t, 15*time.Minute, cfg.Pool.IdleTimeout)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv()
			for k, v := range tt.envVars {
				require.NoError(t, os.Setenv(k, v))
			}

			cfg := ConfigFromEnv()
			require.NotNil(t, cfg, "ConfigFromEnv should not return nil")
			tt.checkFunc(t, cfg)
		})
	}

	// Cleanup
	t.Cleanup(clearEnv)
}

func TestConfigFromEnv_InvalidDuration(t *testing.T) {
	clearEnv := func() {
		os.Unsetenv("TENANT_CACHE_TTL")
		os.Unsetenv("TENANT_POOL_IDLE_TIMEOUT")
	}

	tests := []struct {
		name      string
		envVars   map[string]string
		checkFunc func(t *testing.T, cfg *Config)
	}{
		{
			name: "Invalid CacheTTL should use default",
			envVars: map[string]string{
				"TENANT_CACHE_TTL": "invalid",
			},
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 24*time.Hour, cfg.CacheTTL, "Should use default CacheTTL on invalid input")
			},
		},
		{
			name: "Invalid Pool.IdleTimeout should use default",
			envVars: map[string]string{
				"TENANT_POOL_IDLE_TIMEOUT": "not-a-duration",
			},
			checkFunc: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 30*time.Minute, cfg.Pool.IdleTimeout, "Should use default IdleTimeout on invalid input")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv()
			for k, v := range tt.envVars {
				require.NoError(t, os.Setenv(k, v))
			}

			cfg := ConfigFromEnv()
			require.NotNil(t, cfg, "ConfigFromEnv should not return nil")
			tt.checkFunc(t, cfg)
		})
	}

	t.Cleanup(clearEnv)
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name        string
		modifyFunc  func(cfg *Config)
		wantErr     bool
		errContains string
	}{
		{
			name: "Valid config with all required fields",
			modifyFunc: func(cfg *Config) {
				cfg.ApplicationName = "midaz"
				cfg.PoolManagerURL = "http://pool-manager:8080"
			},
			wantErr: false,
		},
		{
			name: "Disabled config should not require ApplicationName or PoolManagerURL",
			modifyFunc: func(cfg *Config) {
				cfg.Enabled = false
				cfg.ApplicationName = ""
				cfg.PoolManagerURL = ""
			},
			wantErr: false,
		},
		{
			name: "Enabled config requires ApplicationName",
			modifyFunc: func(cfg *Config) {
				cfg.Enabled = true
				cfg.ApplicationName = ""
				cfg.PoolManagerURL = "http://pool-manager:8080"
			},
			wantErr:     true,
			errContains: "application name",
		},
		{
			name: "Enabled config requires PoolManagerURL",
			modifyFunc: func(cfg *Config) {
				cfg.Enabled = true
				cfg.ApplicationName = "midaz"
				cfg.PoolManagerURL = ""
			},
			wantErr:     true,
			errContains: "pool manager URL",
		},
		{
			name: "CacheTTL must be positive",
			modifyFunc: func(cfg *Config) {
				cfg.ApplicationName = "midaz"
				cfg.PoolManagerURL = "http://pool-manager:8080"
				cfg.CacheTTL = -1 * time.Hour
			},
			wantErr:     true,
			errContains: "cache TTL",
		},
		{
			name: "Pool.MaxSize must be positive",
			modifyFunc: func(cfg *Config) {
				cfg.ApplicationName = "midaz"
				cfg.PoolManagerURL = "http://pool-manager:8080"
				cfg.Pool.MaxSize = 0
			},
			wantErr:     true,
			errContains: "pool max size",
		},
		{
			name: "Pool.IdleTimeout must be positive",
			modifyFunc: func(cfg *Config) {
				cfg.ApplicationName = "midaz"
				cfg.PoolManagerURL = "http://pool-manager:8080"
				cfg.Pool.IdleTimeout = -1 * time.Minute
			},
			wantErr:     true,
			errContains: "pool idle timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			require.NotNil(t, cfg)
			tt.modifyFunc(cfg)

			err := cfg.Validate()
			if tt.wantErr {
				require.Error(t, err, "Expected validation error")
				assert.Contains(t, err.Error(), tt.errContains, "Error should contain expected message")
			} else {
				require.NoError(t, err, "Expected no validation error")
			}
		})
	}
}

func TestPoolConfig_Defaults(t *testing.T) {
	tests := []struct {
		name     string
		pool     PoolConfig
		expected PoolConfig
	}{
		{
			name: "Zero values should get defaults",
			pool: PoolConfig{},
			expected: PoolConfig{
				MaxSize:     100,
				IdleTimeout: 30 * time.Minute,
			},
		},
		{
			name: "Non-zero values should be preserved",
			pool: PoolConfig{
				MaxSize:     50,
				IdleTimeout: 15 * time.Minute,
			},
			expected: PoolConfig{
				MaxSize:     50,
				IdleTimeout: 15 * time.Minute,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := tt.pool
			pool.ApplyDefaults()
			assert.Equal(t, tt.expected.MaxSize, pool.MaxSize)
			assert.Equal(t, tt.expected.IdleTimeout, pool.IdleTimeout)
		})
	}
}

func TestConfig_String(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ApplicationName = "test-app"
	cfg.PoolManagerURL = "http://localhost:8080"

	str := cfg.String()
	require.NotEmpty(t, str, "String representation should not be empty")

	// Should contain key configuration values (but not sensitive data)
	assert.Contains(t, str, "test-app", "Should contain application name")
	assert.Contains(t, str, "http://localhost:8080", "Should contain service URL")
}
