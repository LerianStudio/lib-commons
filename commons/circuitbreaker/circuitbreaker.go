// Package circuitbreaker implements the circuit breaker pattern for fault tolerance.
// It provides protection against cascading failures in distributed systems.
package circuitbreaker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// State represents the state of the circuit breaker
type State int

const (
	StateClosed State = iota
	StateOpen
	StateHalfOpen
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// ErrCircuitOpen is returned when the circuit breaker is open
var ErrCircuitOpen = errors.New("circuit breaker is open")

// StateChange represents a state transition
type StateChange struct {
	From State
	To   State
	When time.Time
}

// Metrics contains circuit breaker statistics
type Metrics struct {
	Requests            int64
	Successes           int64
	Failures            int64
	Rejections          int64
	ConsecutiveFailures int64
	LastFailureTime     time.Time
}

// CircuitBreaker implements the circuit breaker pattern
type CircuitBreaker struct {
	name   string
	config *config

	mu                   sync.RWMutex
	state                State
	failures             int64
	consecutiveSuccesses int64
	lastFailureTime      time.Time

	// Metrics
	requests      atomic.Int64
	successes     atomic.Int64
	totalFailures atomic.Int64
	rejections    atomic.Int64
}

// config holds circuit breaker configuration
type config struct {
	threshold        int64
	successThreshold int64
	timeout          time.Duration
	failureCondition func(error) bool
	onStateChange    func(StateChange)
}

// Option configures the circuit breaker
type Option func(*config)

// WithThreshold sets the failure threshold before opening
func WithThreshold(n int64) Option {
	return func(c *config) {
		c.threshold = n
	}
}

// WithSuccessThreshold sets successes needed to close from half-open
func WithSuccessThreshold(n int64) Option {
	return func(c *config) {
		c.successThreshold = n
	}
}

// WithTimeout sets the timeout before trying half-open
func WithTimeout(d time.Duration) Option {
	return func(c *config) {
		c.timeout = d
	}
}

// WithFailureCondition sets a custom function to determine failures
func WithFailureCondition(fn func(error) bool) Option {
	return func(c *config) {
		c.failureCondition = fn
	}
}

// WithOnStateChange sets a callback for state changes
func WithOnStateChange(fn func(StateChange)) Option {
	return func(c *config) {
		c.onStateChange = fn
	}
}

// New creates a new circuit breaker
func New(name string, opts ...Option) *CircuitBreaker {
	cfg := &config{
		threshold:        5,
		successThreshold: 1,
		timeout:          60 * time.Second,
		failureCondition: func(err error) bool {
			return err != nil
		},
		onStateChange: func(StateChange) {},
	}

	for _, opt := range opts {
		opt(cfg)
	}

	return &CircuitBreaker{
		name:   name,
		config: cfg,
		state:  StateClosed,
	}
}

// Name returns the circuit breaker name
func (cb *CircuitBreaker) Name() string {
	return cb.name
}

// State returns the current state
func (cb *CircuitBreaker) State() State {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	// Check if we should transition from open to half-open
	if cb.state == StateOpen && time.Since(cb.lastFailureTime) > cb.config.timeout {
		return StateHalfOpen
	}

	return cb.state
}

// Execute runs the given function if the circuit breaker allows it
func (cb *CircuitBreaker) Execute(fn func() error) error {
	return cb.ExecuteWithContext(context.Background(), fn)
}

// ExecuteWithContext runs the given function with context support
func (cb *CircuitBreaker) ExecuteWithContext(ctx context.Context, fn func() error) error {
	// Check context first
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	cb.requests.Add(1)

	// Check current state
	state := cb.State()

	// Update internal state if needed
	cb.mu.Lock()
	if cb.state == StateOpen && state == StateHalfOpen {
		cb.changeState(StateHalfOpen)
	}

	currentState := cb.state
	cb.mu.Unlock()

	// Handle based on state
	switch currentState {
	case StateOpen:
		cb.rejections.Add(1)
		return ErrCircuitOpen

	case StateHalfOpen:
		// Allow the request but monitor closely
		err := fn()
		cb.recordResult(err, true)

		return err

	case StateClosed:
		// Normal operation
		err := fn()
		cb.recordResult(err, false)

		return err

	default:
		return errors.New("unknown circuit breaker state")
	}
}

// recordResult records the result of an execution
func (cb *CircuitBreaker) recordResult(err error, isHalfOpen bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	isFailure := cb.config.failureCondition(err)

	if isFailure {
		cb.totalFailures.Add(1)

		cb.failures++
		cb.consecutiveSuccesses = 0
		cb.lastFailureTime = time.Now()

		if isHalfOpen {
			// Failed in half-open, go back to open
			cb.changeState(StateOpen)
		} else if cb.failures >= cb.config.threshold {
			// Threshold reached, open the circuit
			cb.changeState(StateOpen)
		}
	} else {
		cb.successes.Add(1)

		cb.consecutiveSuccesses++

		if isHalfOpen && cb.consecutiveSuccesses >= cb.config.successThreshold {
			// Enough successes in half-open, close the circuit
			cb.changeState(StateClosed)
			cb.failures = 0
			cb.consecutiveSuccesses = 0
		} else if !isHalfOpen {
			// Success in closed state resets failure count
			cb.failures = 0
		}
	}
}

// changeState changes the state and notifies observers
func (cb *CircuitBreaker) changeState(newState State) {
	if cb.state == newState {
		return
	}

	change := StateChange{
		From: cb.state,
		To:   newState,
		When: time.Now(),
	}

	cb.state = newState

	// Notify observer (in goroutine to avoid blocking)
	go cb.config.onStateChange(change)
}

// Reset manually resets the circuit breaker to closed state
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.changeState(StateClosed)
	cb.failures = 0
	cb.consecutiveSuccesses = 0
}

// Metrics returns current metrics
func (cb *CircuitBreaker) Metrics() Metrics {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	return Metrics{
		Requests:            cb.requests.Load(),
		Successes:           cb.successes.Load(),
		Failures:            cb.totalFailures.Load(),
		Rejections:          cb.rejections.Load(),
		ConsecutiveFailures: cb.failures,
		LastFailureTime:     cb.lastFailureTime,
	}
}
