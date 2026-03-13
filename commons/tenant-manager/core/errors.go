package core

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// ErrNilHandlerFunc is returned when a nil HandlerFunc is registered.
var ErrNilHandlerFunc = errors.New("handler function must not be nil")

// ErrNilCache is returned when a typed-nil cache implementation is provided.
var ErrNilCache = errors.New("cache implementation must not be nil (received typed-nil interface)")

// ErrNilConfig is returned when a required configuration pointer is nil.
var ErrNilConfig = errors.New("configuration must not be nil")

// ErrInsecureHTTP is returned when an HTTP URL is used without explicit opt-in.
var ErrInsecureHTTP = errors.New("insecure HTTP is not allowed; use HTTPS or enable WithAllowInsecureHTTP()")

// ErrServiceAPIKeyRequired is returned when NewClient is called without a non-empty service API key.
var ErrServiceAPIKeyRequired = errors.New("service API key is required: use WithServiceAPIKey() with a non-empty key")

// IsNilInterface reports whether v is a nil interface value or an interface
// wrapping a nil pointer (typed-nil). This is necessary because Go interfaces
// with a nil concrete value are not == nil.
func IsNilInterface(v any) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		return rv.IsNil()
	default:
		return false
	}
}

// ErrTenantNotFound is returned when the tenant is not found in Tenant Manager.
var ErrTenantNotFound = errors.New("tenant not found")

// ErrServiceNotConfigured is returned when the service is not configured for the tenant.
var ErrServiceNotConfigured = errors.New("service not configured for tenant")

// ErrTenantServiceAccessDenied is returned when the tenant-service association exists
// but is not active (e.g., suspended or purged), resulting in an HTTP 403 from the Tenant Manager.
var ErrTenantServiceAccessDenied = errors.New("tenant service access denied")

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

// ErrAuthorizationTokenRequired is returned when the Authorization header is missing.
var ErrAuthorizationTokenRequired = errors.New("authorization token is required")

// ErrInvalidAuthorizationToken is returned when the JWT token cannot be parsed.
var ErrInvalidAuthorizationToken = errors.New("invalid authorization token")

// ErrInvalidTenantClaims is returned when JWT claims are malformed.
var ErrInvalidTenantClaims = errors.New("invalid tenant claims")

// ErrMissingTenantIDClaim is returned when JWT does not include tenantId.
var ErrMissingTenantIDClaim = errors.New("tenantId claim is required")

// ErrConnectionFailed is returned when tenant DB connection resolution fails.
var ErrConnectionFailed = errors.New("tenant connection failed")

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
	if e == nil {
		return "tenant service is unavailable"
	}

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

// IsTenantPurgedError checks whether err (or any error in its chain) is a
// *TenantSuspendedError whose Status is "purged". This allows callers to
// distinguish purged tenants from suspended ones for eviction decisions.
func IsTenantPurgedError(err error) bool {
	var target *TenantSuspendedError
	if errors.As(err, &target) {
		return target.Status == "purged"
	}

	return false
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
