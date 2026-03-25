// Copyright 2025 Lerian Studio.

package catalog

import "github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"

var postgresKeys = []SharedKey{
	// Primary connection — BundleRebuild, component: "postgres"
	{Key: "postgres.primary_host", EnvVar: "POSTGRES_HOST", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "postgres", Group: "postgres.primary", Description: "PostgreSQL primary host"},
	{Key: "postgres.primary_port", EnvVar: "POSTGRES_PORT", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "postgres", Group: "postgres.primary", Description: "PostgreSQL primary port"},
	{Key: "postgres.primary_user", EnvVar: "POSTGRES_USER", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "postgres", Group: "postgres.primary", Description: "PostgreSQL primary user"},
	{Key: "postgres.primary_password", EnvVar: "POSTGRES_PASSWORD", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "postgres", Group: "postgres.primary", Secret: true, RedactPolicy: domain.RedactFull, Description: "PostgreSQL primary password"},
	{Key: "postgres.primary_db", EnvVar: "POSTGRES_DB", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "postgres", Group: "postgres.primary", Description: "PostgreSQL primary database name"},
	{Key: "postgres.primary_ssl_mode", EnvVar: "POSTGRES_SSLMODE", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "postgres", Group: "postgres.primary", Description: "PostgreSQL primary SSL mode"},

	// Replica connection — BundleRebuild, component: "postgres"
	{Key: "postgres.replica_host", EnvVar: "POSTGRES_REPLICA_HOST", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "postgres", Group: "postgres.replica", Description: "PostgreSQL replica host"},
	{Key: "postgres.replica_port", EnvVar: "POSTGRES_REPLICA_PORT", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "postgres", Group: "postgres.replica", Description: "PostgreSQL replica port"},
	{Key: "postgres.replica_user", EnvVar: "POSTGRES_REPLICA_USER", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "postgres", Group: "postgres.replica", Description: "PostgreSQL replica user"},
	{Key: "postgres.replica_password", EnvVar: "POSTGRES_REPLICA_PASSWORD", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "postgres", Group: "postgres.replica", Secret: true, RedactPolicy: domain.RedactFull, Description: "PostgreSQL replica password"},
	{Key: "postgres.replica_db", EnvVar: "POSTGRES_REPLICA_DB", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "postgres", Group: "postgres.replica", Description: "PostgreSQL replica database name"},
	{Key: "postgres.replica_ssl_mode", EnvVar: "POSTGRES_REPLICA_SSLMODE", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "postgres", Group: "postgres.replica", Description: "PostgreSQL replica SSL mode"},

	// Pool tuning — CANONICAL: LiveRead (NOT BundleRebuild)
	// Go's database/sql supports SetMaxOpenConns() etc. at runtime without teardown.
	{Key: "postgres.max_open_conns", EnvVar: "POSTGRES_MAX_OPEN_CONNS", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone, Group: "postgres.pool", Description: "Maximum open database connections"},
	{Key: "postgres.max_idle_conns", EnvVar: "POSTGRES_MAX_IDLE_CONNS", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone, Group: "postgres.pool", Description: "Maximum idle database connections"},
	{Key: "postgres.conn_max_lifetime_mins", EnvVar: "POSTGRES_CONN_MAX_LIFETIME_MINS", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone, Group: "postgres.pool", Description: "Maximum connection lifetime in minutes"},
	{Key: "postgres.conn_max_idle_time_mins", EnvVar: "POSTGRES_CONN_MAX_IDLE_TIME_MINS", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone, Group: "postgres.pool", Description: "Maximum idle time in minutes"},

	// Connection bootstrap/rebuild tuning
	{Key: "postgres.connect_timeout_sec", EnvVar: "POSTGRES_CONNECT_TIMEOUT_SEC", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "postgres", Group: "postgres.pool", Description: "Connection timeout in seconds"},
	{Key: "postgres.migrations_path", EnvVar: "MIGRATIONS_PATH", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBootstrapOnly, MutableAtRuntime: false, Component: domain.ComponentNone, Group: "postgres", Description: "Database migrations directory path"},
}

// PostgresKeys returns canonical keys for PostgreSQL configuration.
func PostgresKeys() []SharedKey {
	return cloneSharedKeys(postgresKeys)
}
