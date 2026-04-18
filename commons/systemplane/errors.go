package systemplane

import "errors"

// Sentinel errors returned by Client methods.
var (
	// ErrClosed is returned when a method is called on a nil or closed Client.
	ErrClosed = errors.New("systemplane: client is closed or nil")

	// ErrNotStarted is returned when a read/write is attempted before Start.
	ErrNotStarted = errors.New("systemplane: client not started")

	// ErrRegisterAfterStart is returned when Register is called after Start.
	ErrRegisterAfterStart = errors.New("systemplane: register called after start")

	// ErrUnknownKey is returned when Get or Set references an unregistered key.
	ErrUnknownKey = errors.New("systemplane: unknown key")

	// ErrValidation is returned when a value fails its registered validator.
	ErrValidation = errors.New("systemplane: validation failed")

	// ErrDuplicateKey is returned when Register is called with a (namespace, key)
	// pair that has already been registered.
	ErrDuplicateKey = errors.New("systemplane: duplicate key")

	// ErrMissingTenantContext is returned when a tenant-scoped read or write is
	// attempted without a tenant ID present in the context. Tenant resolution
	// is fail-closed: there is no fallback to a shared global for tenant-scoped
	// keys.
	ErrMissingTenantContext = errors.New("systemplane: missing tenant ID in context")

	// ErrInvalidTenantID is returned when the tenant ID extracted from context
	// fails validation (empty, too long, or otherwise malformed). Tenant IDs
	// must satisfy core.IsValidTenantID and cannot collide with the "_global"
	// sentinel that identifies shared rows.
	ErrInvalidTenantID = errors.New("systemplane: invalid tenant ID")

	// ErrTenantScopeNotRegistered is returned when a tenant-scoped operation
	// (GetForTenant, SetForTenant, DeleteForTenant) references a key that was
	// registered via Register rather than RegisterTenantScoped. Globals-only
	// keys cannot accept tenant overrides.
	ErrTenantScopeNotRegistered = errors.New("systemplane: key was not registered via RegisterTenantScoped")
)
