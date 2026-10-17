package circuitbreaker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	"github.com/LerianStudio/lib-commons/v7/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-commons/v7/commons/safe"
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
	// openUntil is the end of the current OPEN window; nil when not OPEN.
	openUntil atomic.Pointer[time.Time]
}

// Zero-value defaults gobreaker v1.0.0 applies in NewCircuitBreaker.
const (
	defaultOpenTimeout         = 60 * time.Second
	defaultHalfOpenMaxRequests = 1
)

// Compile-time guarantees that *manager satisfies every interface tier.
var (
	_ Manager            = (*manager)(nil)
	_ TenantAwareManager = (*manager)(nil)
	_ AdmissionReader    = (*manager)(nil)
)

type manager struct {
	slots           map[breakerMapKey]*breakerSlot
	listeners       []StateChangeListener
	tenantListeners []TenantStateChangeListener
	mu              sync.RWMutex
	logger          obs.Logger
	metricsRecorder obs.MetricsRecorder
	listenerSem     chan struct{}
}

// ManagerOption configures optional behaviour on a circuit breaker manager.
type ManagerOption func(*manager)

// NewManager creates a new circuit breaker manager.
// Returns an error if logger is nil (including typed-nil interface values).
//
// The returned Manager also satisfies TenantAwareManager; callers can
// type-assert when they need the tenant-keyed overloads.
func NewManager(logger obs.Logger, opts ...ManagerOption) (Manager, error) {
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

	slot = m.newSlot(tenantID, serviceName, config)
	m.slots[key] = slot

	m.logger.Log(
		context.Background(),
		obs.LevelInfo,
		"created circuit breaker",
		"service", serviceName,
		"tenant_hash", tenantHashLabel(tenantID),
	)

	return &circuitBreaker{breaker: slot.breaker}, nil
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
				obs.LevelWarn,
				"circuit breaker is OPEN, request rejected",
				"service", serviceName,
				"tenant_hash", tenantHashLabel(tenantID),
			)
			m.recordExecution(slot, executionResultRejectedOpen)

			return nil, fmt.Errorf("service %s is currently unavailable (circuit breaker open): %w", serviceName, err)
		}

		if errors.Is(err, gobreaker.ErrTooManyRequests) {
			m.logger.Log(
				ctx,
				obs.LevelWarn,
				"circuit breaker is HALF-OPEN, too many test requests",
				"service", serviceName,
				"tenant_hash", tenantHashLabel(tenantID),
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
		obs.LevelDebug,
		"health check result",
		"service", serviceName,
		"tenant_hash", tenantHashLabel(tenantID),
		"state", string(state),
		"healthy", isHealthy,
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
			obs.LevelWarn,
			"invalid breaker identity for reset; ignoring request",
			"service", serviceName,
			"tenant_hash", tenantHashLabel(tenantID),
			"error", err,
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
		obs.LevelInfo,
		"resetting circuit breaker",
		"service", serviceName,
		"tenant_hash", tenantHashLabel(tenantID),
	)

	m.slots[key] = m.newSlot(slot.tenantID, slot.serviceName, slot.config)

	m.logger.Log(
		context.Background(),
		obs.LevelInfo,
		"circuit breaker reset completed",
		"service", serviceName,
		"tenant_hash", tenantHashLabel(tenantID),
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
		m.logger.Log(context.Background(), obs.LevelWarn, "attempted to register a nil state change listener")

		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.listeners = append(m.listeners, listener)
	m.logger.Log(context.Background(), obs.LevelDebug, "registered state change listener", "total", len(m.listeners))
}

// RegisterTenantStateChangeListener registers a listener that fires on every
// breaker transition across every tenant. Both untyped nil and typed nil
// (e.g., (*MyListener)(nil)) are rejected with a warning log.
func (m *manager) RegisterTenantStateChangeListener(listener TenantStateChangeListener) {
	if nilcheck.Interface(listener) {
		m.logger.Log(context.Background(), obs.LevelWarn, "attempted to register a nil tenant state change listener")

		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.tenantListeners = append(m.tenantListeners, listener)
	m.logger.Log(context.Background(), obs.LevelDebug, "registered tenant state change listener", "total", len(m.tenantListeners))
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

// newSlot builds a fresh breaker for (tenantID, serviceName). The caller
// holds m.mu for writing and stores the slot.
func (m *manager) newSlot(tenantID, serviceName string, config Config) *breakerSlot {
	slot := &breakerSlot{
		tenantID:    tenantID,
		serviceName: serviceName,
		config:      config,
		metrics:     m.buildBreakerMetrics(tenantID, serviceName),
	}
	slot.breaker = gobreaker.NewCircuitBreaker(m.buildSettings(slot))

	return slot
}

// buildSettings creates gobreaker.Settings for the slot. The underlying
// gobreaker.Name encodes only the stable tenant hash so internal gobreaker
// diagnostics never expose raw tenant IDs; the OnStateChange callback closes
// over the slot for internal routing and tenant-listener API delivery.
func (m *manager) buildSettings(slot *breakerSlot) gobreaker.Settings {
	name := "service-" + slot.serviceName
	if slot.tenantID != "" {
		name = "tenant-" + tenantHashLabel(slot.tenantID) + "/" + name
	}

	return gobreaker.Settings{
		Name:        name,
		MaxRequests: slot.config.MaxRequests,
		Interval:    slot.config.Interval,
		Timeout:     slot.config.Timeout,
		ReadyToTrip: readyToTrip(slot.config),
		OnStateChange: func(_ string, from gobreaker.State, to gobreaker.State) {
			slot.recordOpenWindow(to)
			m.handleStateChange(slot.tenantID, slot.serviceName, from, to)
		},
	}
}

// recordOpenWindow runs inside gobreaker's state lock, so the deadline is set
// before any reader can observe OPEN, at or just after gobreaker's own expiry.
func (s *breakerSlot) recordOpenWindow(to gobreaker.State) {
	if to != gobreaker.StateOpen {
		s.openUntil.Store(nil)

		return
	}

	timeout := s.config.Timeout
	if timeout <= 0 {
		timeout = defaultOpenTimeout
	}

	deadline := time.Now().Add(timeout)
	s.openUntil.Store(&deadline)
}

// Admission reports whether the no-tenant breaker would refuse a call now.
// See AdmissionReader.
func (m *manager) Admission(serviceName string) (time.Time, bool) {
	return m.admission("", serviceName, false)
}

// AdmissionForTenant reports whether the (tenantID, serviceName) breaker
// would refuse a call now. See AdmissionReader.
func (m *manager) AdmissionForTenant(tenantID, serviceName string) (time.Time, bool) {
	return m.admission(tenantID, serviceName, true)
}

func (m *manager) admission(tenantID, serviceName string, tenantAware bool) (time.Time, bool) {
	key, err := validateBreakerIdentity(tenantID, serviceName, tenantAware)
	if err != nil {
		return time.Time{}, false
	}

	m.mu.RLock()
	slot, exists := m.slots[key]
	m.mu.RUnlock()

	if !exists {
		return time.Time{}, false
	}

	// Outside m.mu: the read may expire the window and fire the state
	// callback, which takes m.mu itself.
	return slot.admission()
}

func (s *breakerSlot) admission() (time.Time, bool) {
	quota := s.config.MaxRequests
	if quota == 0 {
		quota = defaultHalfOpenMaxRequests
	}

	// Counts precede State so traffic of a CLOSED generation is never charged to the probe quota.
	requests := s.breaker.Counts().Requests
	// State takes gobreaker's lock and expires an elapsed OPEN window.
	full := s.breaker.State() == gobreaker.StateHalfOpen && requests >= quota

	// Loaded last: a window opened during the reads above wins.
	if until := s.openUntil.Load(); until != nil {
		return *until, true
	}

	return time.Time{}, full
}
