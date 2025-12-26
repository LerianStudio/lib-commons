package poolmanager

import "errors"

// Tenant-related errors for consistent error handling across services.
var (
	// ErrTenantNotFound indicates that the requested tenant does not exist.
	ErrTenantNotFound = errors.New("tenant not found")

	// ErrTenantAlreadyExists indicates that a tenant with the given identifier already exists.
	ErrTenantAlreadyExists = errors.New("tenant already exists")

	// ErrInvalidTenantID indicates that the provided tenant ID is invalid or malformed.
	ErrInvalidTenantID = errors.New("invalid tenant ID")

	// ErrTenantContextMissing indicates that tenant context is required but not present.
	ErrTenantContextMissing = errors.New("tenant context missing")

	// ErrTenantInactive indicates that the tenant exists but is in an inactive state.
	ErrTenantInactive = errors.New("tenant is inactive")

	// ErrTenantSuspended indicates that the tenant has been suspended.
	ErrTenantSuspended = errors.New("tenant is suspended")

	// ErrTenantDeleted indicates that the tenant has been deleted.
	ErrTenantDeleted = errors.New("tenant is deleted")

	// ErrCrossTenantAccess indicates an attempt to access resources belonging to another tenant.
	ErrCrossTenantAccess = errors.New("cross-tenant access denied")

	// ErrTenantIsolationViolation indicates a breach of tenant isolation boundaries.
	ErrTenantIsolationViolation = errors.New("tenant isolation violation")

	// ErrTenantConnectionRequired is returned when multi-tenant mode is enabled
	// but no tenant connection was found in context. This is a critical error
	// that prevents data from being written to the wrong database.
	ErrTenantConnectionRequired = errors.New("tenant connection required but not found in context")

	// ErrNoConnectionAvailable is returned when no database connection is available.
	ErrNoConnectionAvailable = errors.New("no database connection available")
)
