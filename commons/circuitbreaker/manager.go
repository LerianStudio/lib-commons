package circuitbreaker

import (
	"fmt"
	"sync"

	"github.com/LerianStudio/lib-commons/v2/commons/log"
	"github.com/sony/gobreaker"
)

type manager struct {
	breakers  map[string]*gobreaker.CircuitBreaker
	configs   map[string]Config // Store configs for safe reset
	listeners []StateChangeListener
	mu        sync.RWMutex
	logger    log.Logger
}

// NewManager creates a new circuit breaker manager
func NewManager(logger log.Logger) Manager {
	return &manager{
		breakers:  make(map[string]*gobreaker.CircuitBreaker),
		configs:   make(map[string]Config),
		listeners: make([]StateChangeListener, 0),
		logger:    logger,
	}
}

func (m *manager) GetOrCreate(serviceName string, config Config) CircuitBreaker {
	m.mu.RLock()
	breaker, exists := m.breakers[serviceName]
	m.mu.RUnlock()

	if exists {
		return &circuitBreaker{breaker: breaker}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if breaker, exists = m.breakers[serviceName]; exists {
		return &circuitBreaker{breaker: breaker}
	}

	// Create new circuit breaker with configuration
	settings := gobreaker.Settings{
		Name:        fmt.Sprintf("service-%s", serviceName),
		MaxRequests: config.MaxRequests,
		Interval:    config.Interval,
		Timeout:     config.Timeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return counts.ConsecutiveFailures >= config.ConsecutiveFailures ||
				(counts.Requests >= config.MinRequests && failureRatio >= config.FailureRatio)
		},
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			m.handleStateChange(serviceName, from, to)
		},
	}

	breaker = gobreaker.NewCircuitBreaker(settings)
	m.breakers[serviceName] = breaker
	m.configs[serviceName] = config // Store config for safe reset

	m.logger.Infof("Created circuit breaker for service: %s", serviceName)

	return &circuitBreaker{breaker: breaker}
}

func (m *manager) Execute(serviceName string, fn func() (any, error)) (any, error) {
	m.mu.RLock()
	breaker, exists := m.breakers[serviceName]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("circuit breaker not found for service: %s (call GetOrCreate first)", serviceName)
	}

	result, err := breaker.Execute(fn)
	if err != nil {
		if err == gobreaker.ErrOpenState {
			m.logger.Warnf("Circuit breaker [%s] is OPEN - request rejected immediately", serviceName)
			return nil, fmt.Errorf("service %s is currently unavailable (circuit breaker open): %w", serviceName, err)
		}

		if err == gobreaker.ErrTooManyRequests {
			m.logger.Warnf("Circuit breaker [%s] is HALF-OPEN - too many test requests", serviceName)
			return nil, fmt.Errorf("service %s is recovering (too many requests): %w", serviceName, err)
		}
	}

	return result, err
}

func (m *manager) GetState(serviceName string) State {
	m.mu.RLock()
	breaker, exists := m.breakers[serviceName]
	m.mu.RUnlock()

	if !exists {
		return StateUnknown
	}

	state := breaker.State()
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

func (m *manager) GetCounts(serviceName string) Counts {
	m.mu.RLock()
	breaker, exists := m.breakers[serviceName]
	m.mu.RUnlock()

	if !exists {
		return Counts{}
	}

	counts := breaker.Counts()

	return Counts{
		Requests:             counts.Requests,
		TotalSuccesses:       counts.TotalSuccesses,
		TotalFailures:        counts.TotalFailures,
		ConsecutiveSuccesses: counts.ConsecutiveSuccesses,
		ConsecutiveFailures:  counts.ConsecutiveFailures,
	}
}

func (m *manager) IsHealthy(serviceName string) bool {
	state := m.GetState(serviceName)
	// Only CLOSED state is considered healthy
	// OPEN and HALF-OPEN both need health checker intervention
	isHealthy := state == StateClosed
	m.logger.Debugf("IsHealthy check: service=%s, state=%s, isHealthy=%v", serviceName, state, isHealthy)

	return isHealthy
}

func (m *manager) Reset(serviceName string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.breakers[serviceName]; exists {
		m.logger.Infof("Resetting circuit breaker for service: %s", serviceName)

		// Get stored config
		config, configExists := m.configs[serviceName]
		if !configExists {
			m.logger.Warnf("No stored config found for service %s, cannot recreate", serviceName)
			delete(m.breakers, serviceName)

			return
		}

		// Recreate circuit breaker with same configuration
		settings := gobreaker.Settings{
			Name:        fmt.Sprintf("service-%s", serviceName),
			MaxRequests: config.MaxRequests,
			Interval:    config.Interval,
			Timeout:     config.Timeout,
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
				return counts.ConsecutiveFailures >= config.ConsecutiveFailures ||
					(counts.Requests >= config.MinRequests && failureRatio >= config.FailureRatio)
			},
			OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
				m.handleStateChange(serviceName, from, to)
			},
		}

		breaker := gobreaker.NewCircuitBreaker(settings)
		m.breakers[serviceName] = breaker

		m.logger.Infof("Circuit breaker reset completed for service: %s", serviceName)
	}
}

// RegisterStateChangeListener registers a listener for state change notifications
func (m *manager) RegisterStateChangeListener(listener StateChangeListener) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.listeners = append(m.listeners, listener)
	m.logger.Debugf("Registered state change listener (total: %d)", len(m.listeners))
}

// handleStateChange processes state changes and notifies listeners
func (m *manager) handleStateChange(serviceName string, from gobreaker.State, to gobreaker.State) {
	// Log state change
	m.logger.Warnf("Circuit Breaker [%s] state changed: %s -> %s",
		serviceName, from.String(), to.String())

	switch to {
	case gobreaker.StateOpen:
		m.logger.Errorf("Circuit Breaker [%s] OPENED - service is unhealthy, requests will fast-fail", serviceName)
	case gobreaker.StateHalfOpen:
		m.logger.Infof("Circuit Breaker [%s] HALF-OPEN - testing service recovery", serviceName)
	case gobreaker.StateClosed:
		m.logger.Infof("Circuit Breaker [%s] CLOSED - service is healthy", serviceName)
	}

	// Notify listeners
	fromState := convertGobreakerState(from)
	toState := convertGobreakerState(to)

	m.mu.RLock()
	listeners := make([]StateChangeListener, len(m.listeners))
	copy(listeners, m.listeners)
	m.mu.RUnlock()

	for _, listener := range listeners {
		// Notify in goroutine to avoid blocking circuit breaker operations
		go listener.OnStateChange(serviceName, fromState, toState)
	}
}

// convertGobreakerState converts gobreaker.State to our State type
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
