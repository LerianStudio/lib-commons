package circuitbreaker

import "context"

// NewPassthroughManager returns a Manager (and TenantAwareManager) that
// pretends every breaker is in StateClosed and always pass-through-executes
// the supplied callback. It is the canonical implementation of a feature-
// flagged kill-switch — consumers that want to bypass circuit-breaker
// semantics for a deployment (e.g., debugging, single-tenant edge
// environments where breaker oscillation is itself the failure mode) can
// swap their real Manager for this one without changing any call sites.
//
// Semantics:
//   - GetOrCreate / GetOrCreateForTenant return a CircuitBreaker that is also pass-through.
//   - Execute / ExecuteForTenant validate identity, invoke fn exactly once, and return its result verbatim.
//     A nil fn returns ErrNilCallback before identity validation (matching the real Manager's contract).
//   - GetState / GetStateForTenant return StateClosed for valid identities and StateUnknown for invalid identities.
//   - GetCounts / GetCountsForTenant always return the zero Counts{}.
//   - IsHealthy / IsHealthyForTenant return true for valid identities and false for invalid identities.
//   - Reset / ResetForTenant / ResetTenant / RemoveTenant are no-ops.
//   - RegisterStateChangeListener / RegisterTenantStateChangeListener are no-ops
//     (no state ever changes).
//   - Inventory returns an empty slice.
//
// Telemetry: this Manager emits NO metrics — the absence is part of the
// contract. Operators distinguishing "breaker bypassed" from "breaker closed"
// should rely on the kill-switch config flag's own startup log line, NOT on
// metric presence/absence.
//
// The returned value satisfies both Manager and TenantAwareManager; type-
// assert to TenantAwareManager when the call site is tenant-aware.
func NewPassthroughManager() Manager {
	return passthroughManager{}
}

// NewPassthroughTenantAwareManager returns the same pass-through implementation
// with a TenantAwareManager return type for tenant-aware dependency injection
// sites that should not need a type assertion.
func NewPassthroughTenantAwareManager() TenantAwareManager {
	return passthroughManager{}
}

// passthroughManager is the zero-state implementation behind
// NewPassthroughManager. It satisfies both Manager and TenantAwareManager
// so a single value works in both single-tenant and multi-tenant call sites.
type passthroughManager struct{}

// Compile-time checks: passthroughManager satisfies both interfaces.
var (
	_ Manager            = passthroughManager{}
	_ TenantAwareManager = passthroughManager{}
)

// passthroughBreaker is the zero-state CircuitBreaker returned by
// passthroughManager.GetOrCreate*. It mirrors the Manager-level pass-through
// semantics at the per-breaker level so callers that retained the
// CircuitBreaker handle (via GetOrCreate) keep the same kill-switch shape.
type passthroughBreaker struct{}

// Compile-time check: passthroughBreaker satisfies CircuitBreaker.
var _ CircuitBreaker = passthroughBreaker{}

// --- Manager (single-tenant) ------------------------------------------------

// GetOrCreate returns a pass-through CircuitBreaker after validating the
// supplied service name and Config to preserve the real Manager contract.
func (passthroughManager) GetOrCreate(serviceName string, config Config) (CircuitBreaker, error) {
	if _, err := validateBreakerIdentity("", serviceName, false); err != nil {
		return nil, err
	}

	if err := config.Validate(); err != nil {
		return nil, err
	}

	return passthroughBreaker{}, nil
}

// Execute invokes fn exactly once and returns its result verbatim. Returns
// ErrNilCallback when fn is nil, matching the real Manager's contract.
func (passthroughManager) Execute(serviceName string, fn func() (any, error)) (any, error) {
	if fn == nil {
		return nil, ErrNilCallback
	}

	if _, err := validateBreakerIdentity("", serviceName, false); err != nil {
		return nil, err
	}

	return fn()
}

// GetState returns StateClosed for valid service names and StateUnknown for
// invalid identities, matching the real Manager's validation behavior.
func (passthroughManager) GetState(serviceName string) State {
	if _, err := validateBreakerIdentity("", serviceName, false); err != nil {
		return StateUnknown
	}

	return StateClosed
}

// GetCounts always returns the zero Counts{} — there is no counter state
// behind the passthrough breaker, and an all-zero record is the closest
// approximation to "I observed nothing" that the Counts type can express.
func (passthroughManager) GetCounts(serviceName string) Counts {
	if _, err := validateBreakerIdentity("", serviceName, false); err != nil {
		return Counts{}
	}

	return Counts{}
}

// IsHealthy returns true for valid service names and false for invalid ones.
func (passthroughManager) IsHealthy(serviceName string) bool {
	if _, err := validateBreakerIdentity("", serviceName, false); err != nil {
		return false
	}

	return true
}

// Reset is a no-op. There is no breaker state to recreate.
func (passthroughManager) Reset(_ string) {}

// RegisterStateChangeListener is a no-op. The passthrough manager never
// changes state, so a listener registered here would never fire — silently
// discarding it preserves the "no metric, no signal" contract.
func (passthroughManager) RegisterStateChangeListener(_ StateChangeListener) {}

// --- TenantAwareManager (multi-tenant) --------------------------------------

// GetOrCreateForTenant returns a pass-through CircuitBreaker after validating
// tenant ID, service name, and Config to preserve the real Manager contract.
func (passthroughManager) GetOrCreateForTenant(tenantID, serviceName string, config Config) (CircuitBreaker, error) {
	if _, err := validateBreakerIdentity(tenantID, serviceName, true); err != nil {
		return nil, err
	}

	if err := config.Validate(); err != nil {
		return nil, err
	}

	return passthroughBreaker{}, nil
}

// ExecuteForTenant invokes fn exactly once and returns its result verbatim.
// Returns ErrNilCallback when fn is nil. The ctx is ignored at this layer —
// fn must honour ctx itself if cancellation matters.
func (passthroughManager) ExecuteForTenant(_ context.Context, tenantID, serviceName string, fn func() (any, error)) (any, error) {
	if fn == nil {
		return nil, ErrNilCallback
	}

	if _, err := validateBreakerIdentity(tenantID, serviceName, true); err != nil {
		return nil, err
	}

	return fn()
}

// GetStateForTenant returns StateClosed for valid tenant/service pairs and
// StateUnknown for invalid identities.
func (passthroughManager) GetStateForTenant(tenantID, serviceName string) State {
	if _, err := validateBreakerIdentity(tenantID, serviceName, true); err != nil {
		return StateUnknown
	}

	return StateClosed
}

// GetCountsForTenant always returns the zero Counts{}.
func (passthroughManager) GetCountsForTenant(tenantID, serviceName string) Counts {
	if _, err := validateBreakerIdentity(tenantID, serviceName, true); err != nil {
		return Counts{}
	}

	return Counts{}
}

// IsHealthyForTenant returns true for valid tenant/service pairs and false for invalid identities.
func (passthroughManager) IsHealthyForTenant(tenantID, serviceName string) bool {
	if _, err := validateBreakerIdentity(tenantID, serviceName, true); err != nil {
		return false
	}

	return true
}

// ResetForTenant is a no-op.
func (passthroughManager) ResetForTenant(_, _ string) {}

// ResetTenant is a no-op.
func (passthroughManager) ResetTenant(_ string) {}

// RemoveTenant is a no-op.
func (passthroughManager) RemoveTenant(_ string) {}

// Inventory returns an empty, non-nil slice — there is no inventory to report.
// Returning a non-nil zero-length slice keeps the JSON shape stable for
// admin endpoints that surface this directly.
func (passthroughManager) Inventory() []TenantBreakerKey {
	return []TenantBreakerKey{}
}

// RegisterTenantStateChangeListener is a no-op.
func (passthroughManager) RegisterTenantStateChangeListener(_ TenantStateChangeListener) {}

// --- CircuitBreaker ---------------------------------------------------------

// Execute invokes fn exactly once and returns its result verbatim. Returns
// ErrNilCallback when fn is nil, matching the real *circuitBreaker contract.
func (passthroughBreaker) Execute(fn func() (any, error)) (any, error) {
	if fn == nil {
		return nil, ErrNilCallback
	}

	return fn()
}

// State always returns StateClosed.
func (passthroughBreaker) State() State {
	return StateClosed
}

// Counts always returns the zero Counts{}.
func (passthroughBreaker) Counts() Counts {
	return Counts{}
}
