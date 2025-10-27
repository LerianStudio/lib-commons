package circuitbreaker

import (
	"context"
	"time"

	"github.com/sony/gobreaker"
)

// Manager manages circuit breakers for external services
type Manager interface {
	// GetOrCreate returns existing circuit breaker or creates a new one
	GetOrCreate(serviceName string, config Config) CircuitBreaker

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
	Interval            time.Duration // Wait time before half-open retry
	Timeout             time.Duration // Execution timeout
	ConsecutiveFailures uint32        // Consecutive failures to trigger open state
	FailureRatio        float64       // Failure ratio to trigger open (e.g., 0.5 for 50%)
	MinRequests         uint32        // Min requests before checking ratio
}

// State represents circuit breaker state
type State string

const (
	StateClosed   State = "closed"
	StateOpen     State = "open"
	StateHalfOpen State = "half-open"
	StateUnknown  State = "unknown"
)

// Counts represents circuit breaker statistics
type Counts struct {
	Requests             uint32
	TotalSuccesses       uint32
	TotalFailures        uint32
	ConsecutiveSuccesses uint32
	ConsecutiveFailures  uint32
}

// circuitBreaker is the internal implementation wrapping gobreaker
type circuitBreaker struct {
	breaker *gobreaker.CircuitBreaker
}

func (cb *circuitBreaker) Execute(fn func() (any, error)) (any, error) {
	return cb.breaker.Execute(fn)
}

func (cb *circuitBreaker) State() State {
	state := cb.breaker.State()
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

func (cb *circuitBreaker) Counts() Counts {
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

// StateChangeListener is notified when circuit breaker state changes
type StateChangeListener interface {
	// OnStateChange is called when a circuit breaker changes state
	OnStateChange(serviceName string, from State, to State)
}
