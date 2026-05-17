package circuitbreaker

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/LerianStudio/lib-commons/v5/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-commons/v5/commons/safe"
	"github.com/LerianStudio/lib-observability/log"
	"github.com/LerianStudio/lib-observability/metrics"
	"github.com/sony/gobreaker"
)

type breakerMapKey struct {
	tenantID    string
	serviceName string
}

// breakerSlot bundles a per-(tenantID, serviceName) breaker with the
// identifying pair so the gobreaker.OnStateChange callback can reconstruct
// both halves of the key without re-parsing the composite string.
type breakerSlot struct {
	tenantID    string
	serviceName string
	breaker     *gobreaker.CircuitBreaker
	config      Config
	metrics     breakerMetrics
}

// Compile-time guarantees that *manager satisfies both interface tiers.
var (
	_ Manager            = (*manager)(nil)
	_ TenantAwareManager = (*manager)(nil)
)

type manager struct {
	slots           map[breakerMapKey]*breakerSlot
	listeners       []StateChangeListener
	tenantListeners []TenantStateChangeListener
	mu              sync.RWMutex
	logger          log.Logger
	metricsFactory  *metrics.MetricsFactory
	stateCounter    *metrics.CounterBuilder
	execCounter     *metrics.CounterBuilder
	listenerSem     chan struct{}
}

// ManagerOption configures optional behaviour on a circuit breaker manager.
type ManagerOption func(*manager)

// NewManager creates a new circuit breaker manager.
// Returns an error if logger is nil (including typed-nil interface values).
//
// The returned Manager also satisfies TenantAwareManager; callers can
// type-assert when they need the tenant-keyed overloads.
func NewManager(logger log.Logger, opts ...ManagerOption) (Manager, error) {
	if nilcheck.Interface(logger) {
		return nil, ErrNilLogger
	}

	m := &manager{
		slots:           make(map[breakerMapKey]*breakerSlot),
		listeners:       make([]StateChangeListener, 0),
		tenantListeners: make([]TenantStateChangeListener, 0),
		logger:          logger,
		listenerSem:     make(chan struct{}, stateChangeListenerConcurrency),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}

	m.initMetricCounters()

	return m, nil
}

// GetOrCreate returns an existing breaker or creates one for the service.
// If a breaker already exists for the name with a different config, ErrConfigMismatch is returned.
//
// Single-tenant shim: uses the process-wide no-tenant breaker scope.
func (m *manager) GetOrCreate(serviceName string, config Config) (CircuitBreaker, error) {
	return m.getOrCreate("", serviceName, config, false)
}

// GetOrCreateForTenant returns the existing breaker for (tenantID, serviceName)
// or creates one with the supplied Config. If a breaker already exists for
// the same pair with a different Config, ErrConfigMismatch is returned.
func (m *manager) GetOrCreateForTenant(tenantID, serviceName string, config Config) (CircuitBreaker, error) {
	return m.getOrCreate(tenantID, serviceName, config, true)
}

func (m *manager) getOrCreate(tenantID, serviceName string, config Config, tenantAware bool) (CircuitBreaker, error) {
	key, err := validateBreakerIdentity(tenantID, serviceName, tenantAware)
	if err != nil {
		return nil, err
	}

	m.mu.RLock()

	slot, exists := m.slots[key]
	if exists {
		breaker := slot.breaker
		storedConfig := slot.config

		m.mu.RUnlock()

		if storedConfig != config {
			return nil, fmt.Errorf(
				"%w: service %q (tenant_hash %q) already registered with different settings",
				ErrConfigMismatch,
				serviceName,
				tenantHashLabel(tenantID),
			)
		}

		return &circuitBreaker{breaker: breaker}, nil
	}

	m.mu.RUnlock()

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("circuit breaker config for service %s (tenant_hash %q): %w", serviceName, tenantHashLabel(tenantID), err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock.
	if slot, exists = m.slots[key]; exists {
		if slot.config != config {
			return nil, fmt.Errorf(
				"%w: service %q (tenant_hash %q) already registered with different settings",
				ErrConfigMismatch,
				serviceName,
				tenantHashLabel(tenantID),
			)
		}

		return &circuitBreaker{breaker: slot.breaker}, nil
	}

	settings := m.buildSettings(tenantID, serviceName, config)
	breaker := gobreaker.NewCircuitBreaker(settings)

	m.slots[key] = &breakerSlot{
		tenantID:    tenantID,
		serviceName: serviceName,
		breaker:     breaker,
		config:      config,
		metrics:     m.buildBreakerMetrics(tenantID, serviceName),
	}

	m.logger.Log(
		context.Background(),
		log.LevelInfo,
		"created circuit breaker",
		log.String("service", serviceName),
		log.String("tenant_hash", tenantHashLabel(tenantID)),
	)

	return &circuitBreaker{breaker: breaker}, nil
}

// Execute runs fn through the named service breaker.
//
// Single-tenant shim: uses the process-wide no-tenant breaker scope.
func (m *manager) Execute(serviceName string, fn func() (any, error)) (any, error) {
	return m.execute(context.Background(), "", serviceName, fn, false)
}

// ExecuteForTenant runs fn through the (tenantID, serviceName) breaker. The
// supplied ctx is used for logging correlation only — fn itself is invoked
// without ctx so the existing CircuitBreaker contract is preserved. fn must
// honour ctx itself if cancellation matters.
func (m *manager) ExecuteForTenant(ctx context.Context, tenantID, serviceName string, fn func() (any, error)) (any, error) {
	return m.execute(ctx, tenantID, serviceName, fn, true)
}

func (m *manager) execute(ctx context.Context, tenantID, serviceName string, fn func() (any, error), tenantAware bool) (any, error) {
	if fn == nil {
		return nil, ErrNilCallback
	}

	key, err := validateBreakerIdentity(tenantID, serviceName, tenantAware)
	if err != nil {
		return nil, err
	}

	m.mu.RLock()
	slot, exists := m.slots[key]

	var breaker *gobreaker.CircuitBreaker
	if exists {
		breaker = slot.breaker
	}

	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf(
			"circuit breaker not found for service: %s (tenant_hash %q) (call GetOrCreate first)",
			serviceName,
			tenantHashLabel(tenantID),
		)
	}

	result, err := breaker.Execute(fn)
	if err != nil {
		if errors.Is(err, gobreaker.ErrOpenState) {
			m.logger.Log(
				ctx,
				log.LevelWarn,
				"circuit breaker is OPEN, request rejected",
				log.String("service", serviceName),
				log.String("tenant_hash", tenantHashLabel(tenantID)),
			)
			m.recordExecution(slot, executionResultRejectedOpen)

			return nil, fmt.Errorf("service %s is currently unavailable (circuit breaker open): %w", serviceName, err)
		}

		if errors.Is(err, gobreaker.ErrTooManyRequests) {
			m.logger.Log(
				ctx,
				log.LevelWarn,
				"circuit breaker is HALF-OPEN, too many test requests",
				log.String("service", serviceName),
				log.String("tenant_hash", tenantHashLabel(tenantID)),
			)
			m.recordExecution(slot, executionResultRejectedHalfOpen)

			return nil, fmt.Errorf("service %s is recovering (too many requests): %w", serviceName, err)
		}

		// The wrapped function returned an error (not a breaker rejection)
		m.recordExecution(slot, executionResultError)

		return result, err
	}

	m.recordExecution(slot, executionResultSuccess)

	return result, err
}

// GetState returns the current state for a service breaker.
//
// Single-tenant shim: reads from the process-wide no-tenant breaker scope.
func (m *manager) GetState(serviceName string) State {
	return m.getState("", serviceName, false)
}

// GetStateForTenant returns the current state for a (tenantID, serviceName) breaker.
func (m *manager) GetStateForTenant(tenantID, serviceName string) State {
	return m.getState(tenantID, serviceName, true)
}

func (m *manager) getState(tenantID, serviceName string, tenantAware bool) State {
	key, err := validateBreakerIdentity(tenantID, serviceName, tenantAware)
	if err != nil {
		return StateUnknown
	}

	m.mu.RLock()
	slot, exists := m.slots[key]

	var breaker *gobreaker.CircuitBreaker
	if exists {
		breaker = slot.breaker
	}

	m.mu.RUnlock()

	if !exists {
		return StateUnknown
	}

	return convertGobreakerState(breaker.State())
}

// GetCounts returns current counters for a service breaker.
//
// Single-tenant shim: reads from the process-wide no-tenant breaker scope.
func (m *manager) GetCounts(serviceName string) Counts {
	return m.getCounts("", serviceName, false)
}

// GetCountsForTenant returns current counters for the (tenantID, serviceName) breaker.
func (m *manager) GetCountsForTenant(tenantID, serviceName string) Counts {
	return m.getCounts(tenantID, serviceName, true)
}

func (m *manager) getCounts(tenantID, serviceName string, tenantAware bool) Counts {
	key, err := validateBreakerIdentity(tenantID, serviceName, tenantAware)
	if err != nil {
		return Counts{}
	}

	m.mu.RLock()
	slot, exists := m.slots[key]

	var breaker *gobreaker.CircuitBreaker
	if exists {
		breaker = slot.breaker
	}

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

// IsHealthy reports whether the service breaker is in a healthy state.
// Both Closed and HalfOpen states are considered healthy: Closed allows all traffic,
// and HalfOpen allows limited probe traffic for recovery verification.
// Open (rejecting all requests) and Unknown (unregistered breaker) are considered unhealthy.
//
// Single-tenant shim: reads from the process-wide no-tenant breaker scope.
func (m *manager) IsHealthy(serviceName string) bool {
	return m.isHealthy("", serviceName, false)
}

// IsHealthyForTenant reports whether the (tenantID, serviceName) breaker is healthy.
// Closed and HalfOpen are healthy; Open and Unknown are not.
func (m *manager) IsHealthyForTenant(tenantID, serviceName string) bool {
	return m.isHealthy(tenantID, serviceName, true)
}

func (m *manager) isHealthy(tenantID, serviceName string, tenantAware bool) bool {
	state := m.getState(tenantID, serviceName, tenantAware)
	isHealthy := state != StateOpen && state != StateUnknown
	m.logger.Log(
		context.Background(),
		log.LevelDebug,
		"health check result",
		log.String("service", serviceName),
		log.String("tenant_hash", tenantHashLabel(tenantID)),
		log.String("state", string(state)),
		log.Bool("healthy", isHealthy),
	)

	return isHealthy
}

// Reset recreates the service breaker with its stored config.
//
// Single-tenant shim: resets the process-wide no-tenant breaker scope.
func (m *manager) Reset(serviceName string) {
	m.reset("", serviceName, false)
}

// ResetForTenant recreates the (tenantID, serviceName) breaker with its stored
// Config. No-op if no breaker is registered for the pair.
func (m *manager) ResetForTenant(tenantID, serviceName string) {
	m.reset(tenantID, serviceName, true)
}

func (m *manager) reset(tenantID, serviceName string, tenantAware bool) {
	key, err := validateBreakerIdentity(tenantID, serviceName, tenantAware)
	if err != nil {
		m.logger.Log(
			context.Background(),
			log.LevelWarn,
			"invalid breaker identity for reset; ignoring request",
			log.String("service", serviceName),
			log.String("tenant_hash", tenantHashLabel(tenantID)),
			log.Err(err),
		)

		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	slot, exists := m.slots[key]
	if !exists {
		return
	}

	m.logger.Log(
		context.Background(),
		log.LevelInfo,
		"resetting circuit breaker",
		log.String("service", serviceName),
		log.String("tenant_hash", tenantHashLabel(tenantID)),
	)

	settings := m.buildSettings(tenantID, serviceName, slot.config)
	m.slots[key] = &breakerSlot{
		tenantID:    slot.tenantID,
		serviceName: slot.serviceName,
		breaker:     gobreaker.NewCircuitBreaker(settings),
		config:      slot.config,
		metrics:     m.buildBreakerMetrics(slot.tenantID, slot.serviceName),
	}

	m.logger.Log(
		context.Background(),
		log.LevelInfo,
		"circuit breaker reset completed",
		log.String("service", serviceName),
		log.String("tenant_hash", tenantHashLabel(tenantID)),
	)
}

// RegisterStateChangeListener registers a listener for state change notifications.
// Both untyped nil and typed nil (e.g., (*MyListener)(nil)) are rejected.
//
// Listeners registered here only observe transitions for breakers in the
// no-tenant scope (tenantID == ""). For multi-tenant observation, use
// RegisterTenantStateChangeListener.
func (m *manager) RegisterStateChangeListener(listener StateChangeListener) {
	if nilcheck.Interface(listener) {
		m.logger.Log(context.Background(), log.LevelWarn, "attempted to register a nil state change listener")

		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.listeners = append(m.listeners, listener)
	m.logger.Log(context.Background(), log.LevelDebug, "registered state change listener", log.Int("total", len(m.listeners)))
}

// RegisterTenantStateChangeListener registers a listener that fires on every
// breaker transition across every tenant. Both untyped nil and typed nil
// (e.g., (*MyListener)(nil)) are rejected with a warning log.
func (m *manager) RegisterTenantStateChangeListener(listener TenantStateChangeListener) {
	if nilcheck.Interface(listener) {
		m.logger.Log(context.Background(), log.LevelWarn, "attempted to register a nil tenant state change listener")

		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.tenantListeners = append(m.tenantListeners, listener)
	m.logger.Log(context.Background(), log.LevelDebug, "registered tenant state change listener", log.Int("total", len(m.tenantListeners)))
}

// readyToTrip builds the trip function for gobreaker.Settings.
func readyToTrip(config Config) func(counts gobreaker.Counts) bool {
	return func(counts gobreaker.Counts) bool {
		// Check consecutive failures (skip if threshold is 0 = disabled)
		if config.ConsecutiveFailures > 0 && counts.ConsecutiveFailures >= config.ConsecutiveFailures {
			return true
		}

		// Check failure ratio (skip if min requests is 0 = disabled)
		if config.MinRequests > 0 && counts.Requests >= config.MinRequests {
			failureRatio := safe.DivideFloat64OrZero(float64(counts.TotalFailures), float64(counts.Requests))
			return failureRatio >= config.FailureRatio
		}

		return false
	}
}

// buildSettings creates gobreaker.Settings from a Config for the given
// (tenantID, serviceName) pair. The underlying gobreaker.Name encodes only the
// stable tenant hash so internal gobreaker diagnostics never expose raw tenant
// IDs; the OnStateChange callback closes over the raw tenantID for internal
// routing and tenant-listener API delivery.
func (m *manager) buildSettings(tenantID, serviceName string, config Config) gobreaker.Settings {
	name := "service-" + serviceName
	if tenantID != "" {
		name = "tenant-" + tenantHashLabel(tenantID) + "/" + name
	}

	return gobreaker.Settings{
		Name:        name,
		MaxRequests: config.MaxRequests,
		Interval:    config.Interval,
		Timeout:     config.Timeout,
		ReadyToTrip: readyToTrip(config),
		OnStateChange: func(_ string, from gobreaker.State, to gobreaker.State) {
			m.handleStateChange(tenantID, serviceName, from, to)
		},
	}
}
