// Package circuitbreaker implements the circuit breaker pattern for fault tolerance
// and protection against cascading failures in distributed systems.
//
// # Overview
//
// The circuit breaker pattern prevents a service from repeatedly attempting operations
// that are likely to fail, allowing the system to recover gracefully from faults.
// This implementation provides:
//   - Three-state circuit breaker (Closed, Open, Half-Open)
//   - Configurable failure thresholds and timeouts
//   - Comprehensive metrics and observability
//   - Context-aware operations with cancellation support
//   - Custom failure condition detection
//   - Thread-safe concurrent operations
//
// # Circuit Breaker States
//
//   - **Closed**: Normal operation, requests are allowed through
//   - **Open**: Failure threshold exceeded, requests are immediately rejected
//   - **Half-Open**: Testing recovery, limited requests are allowed
//
// # State Transitions
//
//	[Closed]
//	    |
//	    | (failures >= threshold)
//	    v
//	[Open] -----> (timeout expires) -----> [Half-Open]
//	    ^                                        |
//	    |                                        |
//	    | (failure in half-open)                 |
//	    |                                        | (success >= threshold)
//	    +----------------------------------------+
//	                                             |
//	                                             v
//	                                         [Closed]
//
// # Quick Start
//
// Basic circuit breaker usage:
//
//	// Create circuit breaker with default settings
//	cb := circuitbreaker.New("external-api")
//
//	// Execute operation with protection
//	err := cb.Execute(func() error {
//	    return callExternalAPI()
//	})
//
//	if err != nil {
//	    if err == circuitbreaker.ErrCircuitOpen {
//	        // Circuit is open, operation was not attempted
//	        log.Warn("Circuit breaker is open, skipping external API call")
//	        return useDefaultValue()
//	    }
//	    // Other error from the operation itself
//	    return err
//	}
//
// # Configuration Options
//
// Customize circuit breaker behavior:
//
//	cb := circuitbreaker.New("database",
//	    circuitbreaker.WithThreshold(10),              // Open after 10 failures
//	    circuitbreaker.WithTimeout(60*time.Second),    // Try half-open after 60s
//	    circuitbreaker.WithSuccessThreshold(3),        // Close after 3 successes
//	    circuitbreaker.WithFailureCondition(func(err error) bool {
//	        // Only count specific errors as failures
//	        return !errors.Is(err, ErrNotFound)
//	    }),
//	    circuitbreaker.WithOnStateChange(func(change StateChange) {
//	        log.Info("Circuit breaker state changed",
//	            "from", change.From,
//	            "to", change.To,
//	            "when", change.When,
//	        )
//	    }),
//	)
//
// # Context-Aware Operations
//
// Use context for timeout and cancellation:
//
//	func CallWithTimeout(ctx context.Context) error {
//	    // Create context with timeout
//	    ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
//	    defer cancel()
//
//	    return cb.ExecuteWithContext(ctx, func() error {
//	        return slowExternalOperation(ctx)
//	    })
//	}
//
// # Metrics and Monitoring
//
// Monitor circuit breaker performance:
//
//	func MonitorCircuitBreaker(cb *circuitbreaker.CircuitBreaker) {
//	    metrics := cb.Metrics()
//
//	    log.Info("Circuit breaker metrics",
//	        "name", cb.Name(),
//	        "state", cb.State(),
//	        "requests", metrics.Requests,
//	        "successes", metrics.Successes,
//	        "failures", metrics.Failures,
//	        "rejections", metrics.Rejections,
//	        "consecutive_failures", metrics.ConsecutiveFailures,
//	        "last_failure", metrics.LastFailureTime,
//	    )
//
//	    // Calculate success rate
//	    if metrics.Requests > 0 {
//	        successRate := float64(metrics.Successes) / float64(metrics.Requests) * 100
//	        log.Info("Success rate", "percentage", successRate)
//	    }
//	}
//
// # Integration with HTTP Services
//
// Protect HTTP client calls:
//
//	type ProtectedHTTPClient struct {
//	    client *http.Client
//	    cb     *circuitbreaker.CircuitBreaker
//	}
//
//	func (p *ProtectedHTTPClient) Get(ctx context.Context, url string) (*http.Response, error) {
//	    var resp *http.Response
//	    err := p.cb.ExecuteWithContext(ctx, func() error {
//	        req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
//	        if err != nil {
//	            return err
//	        }
//
//	        resp, err = p.client.Do(req)
//	        if err != nil {
//	            return err
//	        }
//
//	        // Treat 5xx status codes as failures
//	        if resp.StatusCode >= 500 {
//	            return fmt.Errorf("server error: %d", resp.StatusCode)
//	        }
//
//	        return nil
//	    })
//
//	    return resp, err
//	}
//
// # Database Connection Protection
//
// Protect database operations:
//
//	func (repo *UserRepository) GetUser(ctx context.Context, id string) (*User, error) {
//	    var user *User
//
//	    err := repo.circuitBreaker.ExecuteWithContext(ctx, func() error {
//	        var err error
//	        user, err = repo.db.GetUser(ctx, id)
//	        return err
//	    })
//
//	    if err == circuitbreaker.ErrCircuitOpen {
//	        // Return cached user or default
//	        return repo.getCachedUser(id)
//	    }
//
//	    return user, err
//	}
//
// # Best Practices
//
//  1. Use appropriate failure thresholds based on service criticality
//  2. Set reasonable timeouts for recovery attempts (30-60 seconds)
//  3. Implement fallback mechanisms for when circuits are open
//  4. Monitor circuit breaker metrics and adjust thresholds as needed
//  5. Use different circuit breakers for different types of operations
//  6. Consider using custom failure conditions for business logic
//  7. Implement graceful degradation when external services are unavailable
//
// # Error Handling
//
// The circuit breaker returns specific errors:
//   - `ErrCircuitOpen`: Circuit is open, operation was not attempted
//   - Original error: Circuit is closed/half-open, operation was attempted but failed
//
// # Thread Safety
//
// All operations are thread-safe and can be used concurrently from multiple goroutines.
// The circuit breaker uses atomic operations and mutexes for safe concurrent access.
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
	// StateClosed represents a closed circuit breaker (normal operation)
	StateClosed State = iota
	// StateOpen represents an open circuit breaker (blocking requests)
	StateOpen
	// StateHalfOpen represents a half-open circuit breaker (testing recovery)
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
