package poolmanager

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds the configuration for tenant management.
type Config struct {
	// Enabled controls whether tenant functionality is active.
	Enabled bool

	// ApplicationName identifies the application using tenant services.
	ApplicationName string

	// PoolManagerURL is the URL of the pool manager API.
	PoolManagerURL string

	// CacheTTL is the duration for caching tenant data.
	// Default: 24h
	CacheTTL time.Duration

	// TenantClaimKey is the JWT claim key used to extract tenant ID.
	// Default: "tenantId"
	TenantClaimKey string

	// Pool contains connection pool configuration.
	Pool PoolConfig
}

// PoolConfig holds connection pool configuration for tenant connections.
type PoolConfig struct {
	// MaxSize is the maximum number of connections in the pool.
	// Default: 100
	MaxSize int

	// IdleTimeout is the duration after which idle connections are closed.
	// Default: 30m
	IdleTimeout time.Duration
}

// DefaultConfig returns a Config with sensible default values.
func DefaultConfig() *Config {
	return &Config{
		Enabled:          true,
		ApplicationName:  "",
		PoolManagerURL: "",
		CacheTTL:         24 * time.Hour,
		TenantClaimKey:   "tenantId",
		Pool: PoolConfig{
			MaxSize:     100,
			IdleTimeout: 30 * time.Minute,
		},
	}
}

// ConfigFromEnv creates a Config populated from environment variables.
// Uses default values when environment variables are not set or invalid.
//
// Environment variables:
//   - TENANT_ENABLED: boolean (default: true)
//   - TENANT_APPLICATION_NAME: string
//   - POOL_MANAGER_URL: string
//   - TENANT_CACHE_TTL: duration string (default: 24h)
//   - TENANT_CLAIM_KEY: string (default: "tenantId")
//   - TENANT_POOL_MAX_SIZE: integer (default: 100)
//   - TENANT_POOL_IDLE_TIMEOUT: duration string (default: 30m)
func ConfigFromEnv() *Config {
	cfg := DefaultConfig()

	// TENANT_ENABLED
	if enabledStr := os.Getenv("TENANT_ENABLED"); enabledStr != "" {
		if enabled, err := strconv.ParseBool(enabledStr); err == nil {
			cfg.Enabled = enabled
		}
	}

	// TENANT_APPLICATION_NAME
	if appName := os.Getenv("TENANT_APPLICATION_NAME"); appName != "" {
		cfg.ApplicationName = appName
	}

	// POOL_MANAGER_URL
	if serviceURL := os.Getenv("POOL_MANAGER_URL"); serviceURL != "" {
		cfg.PoolManagerURL = serviceURL
	}

	// TENANT_CACHE_TTL
	if cacheTTLStr := os.Getenv("TENANT_CACHE_TTL"); cacheTTLStr != "" {
		if cacheTTL, err := time.ParseDuration(cacheTTLStr); err == nil {
			cfg.CacheTTL = cacheTTL
		}
	}

	// TENANT_CLAIM_KEY
	if claimKey := os.Getenv("TENANT_CLAIM_KEY"); claimKey != "" {
		cfg.TenantClaimKey = claimKey
	}

	// TENANT_POOL_MAX_SIZE
	if maxSizeStr := os.Getenv("TENANT_POOL_MAX_SIZE"); maxSizeStr != "" {
		if maxSize, err := strconv.Atoi(maxSizeStr); err == nil {
			cfg.Pool.MaxSize = maxSize
		}
	}

	// TENANT_POOL_IDLE_TIMEOUT
	if idleTimeoutStr := os.Getenv("TENANT_POOL_IDLE_TIMEOUT"); idleTimeoutStr != "" {
		if idleTimeout, err := time.ParseDuration(idleTimeoutStr); err == nil {
			cfg.Pool.IdleTimeout = idleTimeout
		}
	}

	return cfg
}

// Validate checks if the Config is valid.
// Returns an error if validation fails.
func (c *Config) Validate() error {
	// When disabled, no validation required
	if !c.Enabled {
		return nil
	}

	// ApplicationName is required when enabled
	if strings.TrimSpace(c.ApplicationName) == "" {
		return fmt.Errorf("application name is required when tenant functionality is enabled")
	}

	// PoolManagerURL is required when enabled
	if strings.TrimSpace(c.PoolManagerURL) == "" {
		return fmt.Errorf("pool manager URL is required when tenant functionality is enabled")
	}

	// CacheTTL must be positive
	if c.CacheTTL <= 0 {
		return fmt.Errorf("cache TTL must be a positive duration")
	}

	// Pool.MaxSize must be positive
	if c.Pool.MaxSize <= 0 {
		return fmt.Errorf("pool max size must be a positive integer")
	}

	// Pool.IdleTimeout must be positive
	if c.Pool.IdleTimeout <= 0 {
		return fmt.Errorf("pool idle timeout must be a positive duration")
	}

	return nil
}

// ApplyDefaults sets default values for zero-valued fields in PoolConfig.
func (p *PoolConfig) ApplyDefaults() {
	if p.MaxSize == 0 {
		p.MaxSize = 100
	}

	if p.IdleTimeout == 0 {
		p.IdleTimeout = 30 * time.Minute
	}
}

// String returns a human-readable string representation of the Config.
func (c *Config) String() string {
	return fmt.Sprintf(
		"Config{Enabled: %t, ApplicationName: %s, PoolManagerURL: %s, CacheTTL: %s, TenantClaimKey: %s, Pool: {MaxSize: %d, IdleTimeout: %s}}",
		c.Enabled,
		c.ApplicationName,
		c.PoolManagerURL,
		c.CacheTTL,
		c.TenantClaimKey,
		c.Pool.MaxSize,
		c.Pool.IdleTimeout,
	)
}
