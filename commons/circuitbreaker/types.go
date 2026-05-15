package circuitbreaker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sony/gobreaker"
)

var (
	// ErrInvalidConfig is returned when a Config has invalid or insufficient values.
	ErrInvalidConfig = errors.New("circuitbreaker: invalid config")

	// ErrNilLogger is returned when a nil logger is passed to NewManager.
	ErrNilLogger = errors.New("circuitbreaker: logger must not be nil")

	// ErrNilCallback is returned when a nil callback is passed to Execute.
	ErrNilCallback = errors.New("circuitbreaker: callback must not be nil")

	// ErrConfigMismatch is returned when GetOrCreate is called with a config that
	// differs from the one stored for an existing breaker with the same name.
	ErrConfigMismatch = errors.New("circuitbreaker: breaker already exists with different config")

	// ErrInvalidServiceName is returned when a service name is empty or contains
	// control characters that would make breaker identity ambiguous in logs/metrics.
	ErrInvalidServiceName = errors.New("circuitbreaker: invalid service name")

	// ErrInvalidTenantID is returned when a tenant-aware API receives an invalid
	// non-empty tenant ID.
	ErrInvalidTenantID = errors.New("circuitbreaker: invalid tenant id")

	// ErrBreakerOpen is returned from Execute when the breaker is OPEN and
	// rejects the request without invoking the callback. Aliases
	// gobreaker.ErrOpenState so callers that previously matched the underlying
	// sentinel continue to work via errors.Is.
	ErrBreakerOpen = gobreaker.ErrOpenState

	// ErrBreakerHalfOpenFull is returned from Execute when the breaker is
	// HALF-OPEN and has exhausted its probe-request quota. Aliases
	// gobreaker.ErrTooManyRequests for the same errors.Is compatibility as
	// ErrBreakerOpen.
	ErrBreakerHalfOpenFull = gobreaker.ErrTooManyRequests
)

// Manager manages circuit breakers for external services.
//
// This is the historical, single-tenant interface. Every implementation in
// this package also satisfies TenantAwareManager, which extends Manager with
// explicit (tenantID, serviceName) overloads for multi-tenant SaaS deployments
// where one process serves N tenants and a single tenant's outage must not
// trip a shared breaker for its neighbors.
//
// New code should accept TenantAwareManager and use the *ForTenant methods.
// The single-argument methods on Manager are preserved verbatim for backward
// compatibility — they internally route to the tenant-aware path with
// tenantID == "" (the "no-tenant / process-wide" key).
type Manager interface {
	// GetOrCreate returns existing circuit breaker or creates a new one.
	// Returns an error if the config is invalid.
	//
	// Single-tenant shim: uses the process-wide no-tenant breaker scope.
	GetOrCreate(serviceName string, config Config) (CircuitBreaker, error)

	// Execute runs a function through the circuit breaker.
	//
	// Single-tenant shim: uses the process-wide no-tenant breaker scope.
	Execute(serviceName string, fn func() (any, error)) (any, error)

	// GetState returns the current state.
	//
	// Single-tenant shim: reads from the process-wide no-tenant breaker scope.
	GetState(serviceName string) State

	// GetCounts returns the current counts for a circuit breaker.
	//
	// Single-tenant shim: reads from the process-wide no-tenant breaker scope.
	GetCounts(serviceName string) Counts

	// IsHealthy returns true if circuit breaker is not open.
	//
	// Single-tenant shim: reads from the process-wide no-tenant breaker scope.
	IsHealthy(serviceName string) bool

	// Reset resets circuit breaker to closed state.
	//
	// Single-tenant shim: resets the process-wide no-tenant breaker scope.
	Reset(serviceName string)

	// RegisterStateChangeListener registers a listener for circuit breaker state changes.
	// Listener callbacks are invoked asynchronously; implementations must be
	// concurrency-safe and should return promptly. Slow listeners are bounded by
	// an internal timeout and must not rely on backpressure to the caller.
	//
	// Listeners registered here only observe transitions for breakers in the
	// no-tenant scope (tenantID == ""). For multi-tenant observation, use
	// TenantAwareManager.RegisterTenantStateChangeListener, which fires on
	// every transition regardless of tenant.
	RegisterStateChangeListener(listener StateChangeListener)
}

// TenantAwareManager extends Manager with explicit (tenantID, serviceName)
// overloads so a single Manager can isolate breakers across tenants sharing
// the same pod.
//
// The internal map keys breakers by (tenantID, serviceName). Tenant-aware
// methods require a non-empty valid tenantID. Use the legacy Manager methods
// when the caller needs the process-wide / no-tenant breaker scope that
// preserves the v5.1.0 single-tenant semantics.
//
// Cardinality envelope: with N tenants × S services × 9 state-transition
// pairs, the circuit_breaker_state_transitions_total metric has cardinality
// N × S × 9. For 100 tenants × 5 services × 9 = 4500 series — well within
// Prometheus / OTel limits but worth being aware of. Metrics and logs expose a
// stable non-reversible tenant_hash instead of the raw tenant ID.
//
// All TenantAwareManager methods are safe for concurrent use.
type TenantAwareManager interface {
	Manager

	// GetOrCreateForTenant returns the existing breaker for (tenantID, serviceName)
	// or creates one with the supplied Config. If a breaker already exists for
	// the same pair with a different Config, returns ErrConfigMismatch.
	GetOrCreateForTenant(tenantID, serviceName string, config Config) (CircuitBreaker, error)

	// ExecuteForTenant runs fn through the (tenantID, serviceName) breaker.
	// The ctx is reserved for future cancellation semantics; it is currently
	// only used for logging correlation and is NOT forwarded to fn. fn must
	// honour ctx itself if cancellation matters.
	//
	// Returns ErrNilCallback when fn is nil.
	ExecuteForTenant(ctx context.Context, tenantID, serviceName string, fn func() (any, error)) (any, error)

	// GetStateForTenant returns the current state of the (tenantID, serviceName)
	// breaker. Returns StateUnknown if no breaker is registered for the pair.
	GetStateForTenant(tenantID, serviceName string) State

	// GetCountsForTenant returns the current counters for the (tenantID, serviceName)
	// breaker. Returns the zero Counts{} if no breaker is registered.
	GetCountsForTenant(tenantID, serviceName string) Counts

	// IsHealthyForTenant reports whether the (tenantID, serviceName) breaker
	// is Closed or HalfOpen. Open and Unknown are both unhealthy.
	IsHealthyForTenant(tenantID, serviceName string) bool

	// ResetForTenant recreates the (tenantID, serviceName) breaker with its
	// stored Config. No-op if no breaker is registered for the pair.
	ResetForTenant(tenantID, serviceName string)

	// ResetTenant recreates every breaker for a given tenant. Useful after a
	// per-tenant credential rotation without disturbing other tenants.
	// No-op if the tenant has no registered breakers.
	ResetTenant(tenantID string)

	// RemoveTenant drops every breaker for the given tenant entirely (no
	// recreation). Use at tenant-deactivation time so stale entries do not
	// hold memory for vanished tenants. No-op if the tenant has no entries.
	RemoveTenant(tenantID string)

	// Inventory returns a snapshot of every (tenantID, serviceName) pair
	// the Manager currently holds. Safe to iterate concurrently with further
	// Manager operations; the snapshot may be stale by the time the caller
	// reads it.
	Inventory() []TenantBreakerKey

	// RegisterTenantStateChangeListener registers a listener that fires on
	// every breaker transition, regardless of tenant. The listener receives
	// the (tenantID, serviceName) pair so a single listener can observe the
	// full fleet. Listener callbacks are invoked asynchronously; implementations
	// must be concurrency-safe and should return promptly.
	RegisterTenantStateChangeListener(listener TenantStateChangeListener)
}

// TenantBreakerKey identifies a single (tenantID, serviceName) pair in the
// Manager's inventory. TenantID is the empty string for breakers registered
// via the single-tenant Manager methods.
type TenantBreakerKey struct {
	TenantID    string
	ServiceName string
}

// CircuitBreaker wraps sony/gobreaker with our interface
type CircuitBreaker interface {
	Execute(fn func() (any, error)) (any, error)
	State() State
	Counts() Counts
}

// Config holds circuit breaker configuration
type Config struct {
	MaxRequests         uint32        // Max requests in half-open state
	Interval            time.Duration // Cyclic period of the closed state to clear internal counts
	Timeout             time.Duration // Period of the open state before becoming half-open
	ConsecutiveFailures uint32        // Consecutive failures to trigger open state
	FailureRatio        float64       // Failure ratio to trigger open (e.g., 0.5 for 50%)
	MinRequests         uint32        // Min requests before checking ratio
}

// Validate checks that the Config has valid values.
// At least one trip condition (ConsecutiveFailures or MinRequests+FailureRatio) must be enabled.
// Interval and Timeout must not be negative.
func (c Config) Validate() error {
	if c.ConsecutiveFailures == 0 && c.MinRequests == 0 {
		return fmt.Errorf("%w: at least one trip condition must be set (ConsecutiveFailures > 0 or MinRequests > 0)", ErrInvalidConfig)
	}

	if c.FailureRatio < 0 || c.FailureRatio > 1 {
		return fmt.Errorf("%w: FailureRatio must be between 0 and 1, got %f", ErrInvalidConfig, c.FailureRatio)
	}

	if c.MinRequests > 0 && c.FailureRatio <= 0 {
		return fmt.Errorf("%w: FailureRatio must be > 0 when MinRequests > 0 (ratio-based trip is ineffective with FailureRatio=0)", ErrInvalidConfig)
	}

	if c.Interval < 0 {
		return fmt.Errorf("%w: Interval must not be negative, got %v", ErrInvalidConfig, c.Interval)
	}

	if c.Timeout < 0 {
		return fmt.Errorf("%w: Timeout must not be negative, got %v", ErrInvalidConfig, c.Timeout)
	}

	return nil
}

// State represents circuit breaker state
type State string

const (
	// StateClosed allows requests to pass through normally.
	StateClosed State = "closed"
	// StateOpen rejects requests until the timeout elapses.
	StateOpen State = "open"
	// StateHalfOpen allows limited trial requests after an open period.
	StateHalfOpen State = "half-open"
	// StateUnknown is returned when the underlying state cannot be mapped.
	StateUnknown State = "unknown"
)

// Counts represents circuit breaker statistics
type Counts struct {
	Requests             uint32
	TotalSuccesses       uint32
	TotalFailures        uint32
	ConsecutiveSuccesses uint32
	ConsecutiveFailures  uint32
}

// ErrNilCircuitBreaker is returned when a circuit breaker method is called on a nil or uninitialized instance.
var ErrNilCircuitBreaker = errors.New("circuitbreaker: not initialized")

// circuitBreaker is the internal implementation wrapping gobreaker
type circuitBreaker struct {
	breaker *gobreaker.CircuitBreaker
}

// Execute runs fn through the underlying circuit breaker.
func (cb *circuitBreaker) Execute(fn func() (any, error)) (any, error) {
	if cb == nil || cb.breaker == nil {
		return nil, ErrNilCircuitBreaker
	}

	if fn == nil {
		return nil, ErrNilCallback
	}

	return cb.breaker.Execute(fn)
}

// State returns the current circuit breaker state.
func (cb *circuitBreaker) State() State {
	if cb == nil || cb.breaker == nil {
		return StateUnknown
	}

	return convertGobreakerState(cb.breaker.State())
}

// Counts returns the current breaker counters.
func (cb *circuitBreaker) Counts() Counts {
	if cb == nil || cb.breaker == nil {
		return Counts{}
	}

	counts := cb.breaker.Counts()

	return Counts{
		Requests:             counts.Requests,
		TotalSuccesses:       counts.TotalSuccesses,
		TotalFailures:        counts.TotalFailures,
		ConsecutiveSuccesses: counts.ConsecutiveSuccesses,
		ConsecutiveFailures:  counts.ConsecutiveFailures,
	}
}

// HealthChecker performs periodic health checks on services and manages circuit breaker recovery
type HealthChecker interface {
	// Register adds a service to health check
	Register(serviceName string, healthCheckFn HealthCheckFunc)

	// Start begins the health check loop in a separate goroutine
	Start()

	// Stop gracefully stops the health checker
	Stop()

	// GetHealthStatus returns the current health status of all services
	GetHealthStatus() map[string]string

	// StateChangeListener interface to receive circuit breaker state change notifications
	StateChangeListener
}

// HealthCheckFunc defines a function that checks service health
type HealthCheckFunc func(ctx context.Context) error

// StateChangeListener is notified when circuit breaker state changes.
//
// Listeners registered via Manager.RegisterStateChangeListener only fire for
// breakers in the no-tenant scope (tenantID == ""). For multi-tenant
// observation, implement TenantStateChangeListener and register it via
// TenantAwareManager.RegisterTenantStateChangeListener instead — those
// listeners fire on every transition regardless of tenant.
type StateChangeListener interface {
	// OnStateChange is called when a circuit breaker changes state.
	// The provided context carries a deadline derived from the listener timeout;
	// implementations should respect ctx.Done() for cancellation.
	OnStateChange(ctx context.Context, serviceName string, from State, to State)
}

// TenantStateChangeListener is the tenant-aware sibling of StateChangeListener.
// A listener registered via TenantAwareManager.RegisterTenantStateChangeListener
// fires on every breaker transition with the (tenantID, serviceName) pair —
// including transitions for breakers in the no-tenant scope, where tenantID
// is the empty string.
//
// Use this when one listener needs to observe transitions across the full
// fleet of tenants served by a process. The provided context carries the
// same per-notification deadline as StateChangeListener.OnStateChange.
type TenantStateChangeListener interface {
	OnTenantStateChange(ctx context.Context, tenantID, serviceName string, from State, to State)
}

// convertGobreakerState converts gobreaker.State to our State type.
func convertGobreakerState(state gobreaker.State) State {
	switch state {
	case gobreaker.StateClosed:
		return StateClosed
	case gobreaker.StateOpen:
		return StateOpen
	case gobreaker.StateHalfOpen:
		return StateHalfOpen
	default:
		return StateUnknown
	}
}
