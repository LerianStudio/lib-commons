package tenantmanager

import (
	"errors"
	"strings"
)

// ErrTenantNotFound is returned when the tenant is not found in Tenant Manager.
var ErrTenantNotFound = errors.New("tenant not found")

// ErrServiceNotConfigured is returned when the service is not configured for the tenant.
var ErrServiceNotConfigured = errors.New("service not configured for tenant")

// ErrModuleNotConfigured is returned when the module is not configured for the service.
var ErrModuleNotConfigured = errors.New("module not configured for service")

// ErrConnectionNotFound is returned when no connection exists for the tenant.
var ErrConnectionNotFound = errors.New("connection not found for tenant")

// ErrPoolClosed is returned when attempting to use a closed pool.
var ErrPoolClosed = errors.New("tenant connection pool is closed")

// ErrTenantContextRequired is returned when no tenant context is found for a database operation.
// This error indicates that a request attempted to access the database without proper tenant identification.
// The tenant connection must be set in context via middleware before database operations.
var ErrTenantContextRequired = errors.New("tenant context required: no tenant database connection found in context")

// ErrTenantNotProvisioned is returned when the tenant database schema has not been initialized.
// This typically happens when migrations have not been run on the tenant's database.
// PostgreSQL error code 42P01 (undefined_table) indicates this condition.
var ErrTenantNotProvisioned = errors.New("tenant database not provisioned: schema has not been initialized")

// IsTenantNotProvisionedError checks if the error indicates an unprovisioned tenant database.
// PostgreSQL returns SQLSTATE 42P01 (undefined_table) when a relation (table) does not exist.
// This typically occurs when migrations have not been run on the tenant database.
func IsTenantNotProvisionedError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()

	// Check for PostgreSQL error code 42P01 (undefined_table)
	// This is the standard SQLSTATE for "relation does not exist"
	if strings.Contains(errStr, "42P01") {
		return true
	}

	// Also check for the common error message pattern
	if strings.Contains(errStr, "relation") && strings.Contains(errStr, "does not exist") {
		return true
	}

	return false
}
