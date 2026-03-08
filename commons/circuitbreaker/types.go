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
)

// Manager manages circuit breakers for external services
type Manager interface {
	// GetOrCreate returns existing circuit breaker or creates a new one.
	// Returns an error if the config is invalid.
	GetOrCreate(serviceName string, config Config) (CircuitBreaker, error)

	// Execute runs a function through the circuit breaker
	Execute(serviceName string, fn func() (any, error)) (any, error)

	// GetState returns the current state
	GetState(serviceName string) State

	// GetCounts returns the current counts for a circuit breaker
	GetCounts(serviceName string) Counts

	// IsHealthy returns true if circuit breaker is not open
	IsHealthy(serviceName string) bool

	// Reset resets circuit breaker to closed state
	Reset(serviceName string)

	// RegisterStateChangeListener registers a listener for circuit breaker state changes
	RegisterStateChangeListener(listener StateChangeListener)
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
type StateChangeListener interface {
	// OnStateChange is called when a circuit breaker changes state.
	// The provided context carries a deadline derived from the listener timeout;
	// implementations should respect ctx.Done() for cancellation.
	OnStateChange(ctx context.Context, serviceName string, from State, to State)
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
