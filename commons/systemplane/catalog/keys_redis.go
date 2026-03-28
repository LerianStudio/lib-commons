// Copyright 2025 Lerian Studio.

package catalog

import "github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"

var redisKeys = []SharedKey{
	// Connection — BundleRebuild, component: "redis"
	{Key: "redis.host", EnvVar: "REDIS_HOST", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis", Group: "redis.connection", Description: "Redis host address"},
	{Key: "redis.master_name", EnvVar: "REDIS_MASTER_NAME", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis", Group: "redis.connection", Description: "Redis Sentinel master name"},
	{Key: "redis.password", EnvVar: "REDIS_PASSWORD", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis", Group: "redis.connection", Secret: true, RedactPolicy: domain.RedactFull, Description: "Redis password"},
	{Key: "redis.db", EnvVar: "REDIS_DB", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis", Group: "redis.connection", Description: "Redis database index"},
	{Key: "redis.protocol", EnvVar: "REDIS_PROTOCOL", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis", Group: "redis.connection", Description: "Redis protocol version"},
	{Key: "redis.tls", EnvVar: "REDIS_TLS", ValueType: domain.ValueTypeBool, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis", Group: "redis.connection", Description: "Enable Redis TLS"},
	{Key: "redis.ca_cert", EnvVar: "REDIS_CA_CERT", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis", Group: "redis.connection", Secret: true, RedactPolicy: domain.RedactFull, Description: "Redis CA certificate"},
	// Pool
	{Key: "redis.pool_size", EnvVar: "REDIS_POOL_SIZE", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis", Group: "redis.pool", Description: "Redis connection pool size"},
	{Key: "redis.min_idle_conns", EnvVar: "REDIS_MIN_IDLE_CONNS", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis", Group: "redis.pool", Description: "Redis minimum idle connections"},
	// Timeouts — BundleRebuild (affect client config)
	{Key: "redis.read_timeout_ms", EnvVar: "REDIS_READ_TIMEOUT_MS", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis", Group: "redis.timeouts", Description: "Redis read timeout in milliseconds"},
	{Key: "redis.write_timeout_ms", EnvVar: "REDIS_WRITE_TIMEOUT_MS", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis", Group: "redis.timeouts", Description: "Redis write timeout in milliseconds"},
	{Key: "redis.dial_timeout_ms", EnvVar: "REDIS_DIAL_TIMEOUT_MS", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis", Group: "redis.timeouts", Description: "Redis dial timeout in milliseconds"},
}

// RedisKeys returns canonical keys for Redis configuration.
func RedisKeys() []SharedKey {
	return cloneSharedKeys(redisKeys)
}
