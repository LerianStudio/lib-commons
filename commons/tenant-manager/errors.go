package tenantmanager

import (
	"errors"
	"fmt"
	"strings"
)

// ErrTenantNotFound is returned when the tenant is not found in Tenant Manager.
var ErrTenantNotFound = errors.New("tenant not found")

// ErrServiceNotConfigured is returned when the service is not configured for the tenant.
var ErrServiceNotConfigured = errors.New("service not configured for tenant")

// ErrManagerClosed is returned when attempting to use a closed connection manager.
var ErrManagerClosed = errors.New("tenant connection manager is closed")

// ErrTenantContextRequired is returned when no tenant context is found for a database operation.
// This error indicates that a request attempted to access the database without proper tenant identification.
// The tenant connection must be set in context via middleware before database operations.
var ErrTenantContextRequired = errors.New("tenant context required: no tenant database connection found in context")

// ErrTenantNotProvisioned is returned when the tenant database schema has not been initialized.
// This typically happens when migrations have not been run on the tenant's database.
// PostgreSQL error code 42P01 (undefined_table) indicates this condition.
var ErrTenantNotProvisioned = errors.New("tenant database not provisioned: schema has not been initialized")

// ErrCircuitBreakerOpen is returned when the circuit breaker is in the open state,
// indicating the Tenant Manager service is temporarily unavailable.
// Callers should retry after the circuit breaker timeout elapses.
var ErrCircuitBreakerOpen = errors.New("tenant manager circuit breaker is open: service temporarily unavailable")

// IsCircuitBreakerOpenError checks whether err (or any error in its chain) is ErrCircuitBreakerOpen.
func IsCircuitBreakerOpenError(err error) bool {
	return errors.Is(err, ErrCircuitBreakerOpen)
}

// TenantSuspendedError is returned when the tenant-service association exists but is not active
// (e.g., suspended or purged). This allows callers to distinguish between "not found" and
// "access denied due to status" scenarios.
type TenantSuspendedError struct {
	TenantID string // The tenant identifier that was requested
	Status   string // The current status (e.g., "suspended", "purged")
	Message  string // Human-readable error message from the server
}

// Error implements the error interface.
func (e *TenantSuspendedError) Error() string {
	if e.Message != "" {
		return e.Message
	}

	return fmt.Sprintf("tenant service is %s for tenant %s", e.Status, e.TenantID)
}

// IsTenantSuspendedError checks whether err (or any error in its chain) is a *TenantSuspendedError.
func IsTenantSuspendedError(err error) bool {
	var target *TenantSuspendedError
	return errors.As(err, &target)
}

// IsTenantNotProvisionedError checks if the error indicates an unprovisioned tenant database.
// It first checks the error chain using errors.Is for the sentinel ErrTenantNotProvisioned,
// then falls back to string matching for PostgreSQL SQLSTATE 42P01 (undefined_table).
// This typically occurs when migrations have not been run on the tenant database.
func IsTenantNotProvisionedError(err error) bool {
	if err == nil {
		return false
	}

	// Prefer errors.Is for wrapped sentinel errors
	if errors.Is(err, ErrTenantNotProvisioned) {
		return true
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
