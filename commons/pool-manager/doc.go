// Package poolmanager provides multi-tenant support for Midaz services.
//
// This package offers utilities for managing tenant context, validation,
// and error handling in multi-tenant applications. It provides:
//   - Tenant context key for request-scoped tenant identification
//   - Standard tenant-related errors for consistent error handling
//   - Tenant isolation utilities to prevent cross-tenant data access
//   - Connection pool management for PostgreSQL, MongoDB, Valkey, and RabbitMQ
package poolmanager

const (
	// PackageName is the name of this package, used for logging and identification.
	PackageName = "tenants"
)

// Note: Tenant context keys are defined in context.go as typed ContextKey constants.
// Use TenantIDContextKey for storing/retrieving tenant ID from context.
