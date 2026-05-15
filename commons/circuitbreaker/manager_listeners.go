package circuitbreaker

import (
	"context"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/runtime"
	"github.com/sony/gobreaker"
)

const (
	// stateChangeListenerTimeout limits how long a state change listener notification
	// can run before the context is cancelled.
	stateChangeListenerTimeout = 10 * time.Second
	// stateChangeListenerConcurrency bounds asynchronous listener fanout during breaker flapping.
	stateChangeListenerConcurrency = 64
)

// handleStateChange processes state changes and notifies listeners.
//
// Notification rules:
//   - Tenant-aware listeners (RegisterTenantStateChangeListener) fire on every
//     transition, regardless of tenantID.
//   - Legacy listeners (RegisterStateChangeListener) fire ONLY for transitions
//     on no-tenant breakers (tenantID == ""). This preserves the pre-tenant
//     semantics for v5.1.0 callers who registered legacy listeners against a
//     single-tenant manager — they continue to see exactly the same set of
//     events.
func (m *manager) handleStateChange(tenantID, serviceName string, from gobreaker.State, to gobreaker.State) {
	switch to {
	case gobreaker.StateOpen:
		m.logger.Log(
			context.Background(),
			log.LevelError,
			"circuit breaker OPENED, requests will fast-fail",
			log.String("service", serviceName),
			log.String("tenant_hash", tenantHashLabel(tenantID)),
			log.String("from", from.String()),
		)
	case gobreaker.StateHalfOpen:
		m.logger.Log(
			context.Background(),
			log.LevelInfo,
			"circuit breaker HALF-OPEN, testing service recovery",
			log.String("service", serviceName),
			log.String("tenant_hash", tenantHashLabel(tenantID)),
			log.String("from", from.String()),
		)
	case gobreaker.StateClosed:
		m.logger.Log(
			context.Background(),
			log.LevelInfo,
			"circuit breaker CLOSED, service is healthy",
			log.String("service", serviceName),
			log.String("tenant_hash", tenantHashLabel(tenantID)),
			log.String("from", from.String()),
		)
	}

	fromState := convertGobreakerState(from)
	toState := convertGobreakerState(to)

	m.recordStateTransition(tenantID, serviceName, fromState, toState)

	m.mu.RLock()
	tenantListeners := make([]TenantStateChangeListener, len(m.tenantListeners))
	copy(tenantListeners, m.tenantListeners)

	var legacyListeners []StateChangeListener
	if tenantID == "" {
		legacyListeners = make([]StateChangeListener, len(m.listeners))
		copy(legacyListeners, m.listeners)
	}

	m.mu.RUnlock()

	for _, listener := range tenantListeners {
		listenerCopy := listener
		m.dispatchStateChangeListener("tenant_state_change_listener_"+serviceName, func(ctx context.Context) {
			m.notifyTenantStateChangeListener(ctx, listenerCopy, tenantID, serviceName, fromState, toState)
		})
	}

	for _, listener := range legacyListeners {
		listenerCopy := listener
		m.dispatchStateChangeListener("state_change_listener_"+serviceName, func(ctx context.Context) {
			m.notifyStateChangeListener(ctx, listenerCopy, serviceName, fromState, toState)
		})
	}
}

func (m *manager) dispatchStateChangeListener(operation string, notify func(context.Context)) {
	select {
	case m.listenerSem <- struct{}{}:
	default:
		m.logger.Log(context.Background(), log.LevelWarn,
			"circuit breaker state-change listener queue saturated; dropping notification",
			log.String("operation", operation),
		)

		return
	}

	runtime.SafeGoWithContextAndComponent(
		context.Background(),
		m.logger,
		"circuitbreaker",
		operation,
		runtime.KeepRunning,
		func(ctx context.Context) {
			defer func() { <-m.listenerSem }()
			notify(ctx)
		},
	)
}

func (m *manager) notifyStateChangeListener(
	ctx context.Context,
	listener StateChangeListener,
	serviceName string,
	fromState State,
	toState State,
) {
	listenerCtx, listenerCancel := context.WithTimeout(ctx, stateChangeListenerTimeout)
	defer listenerCancel()

	listener.OnStateChange(listenerCtx, serviceName, fromState, toState)
}

func (m *manager) notifyTenantStateChangeListener(
	ctx context.Context,
	listener TenantStateChangeListener,
	tenantID string,
	serviceName string,
	fromState State,
	toState State,
) {
	listenerCtx, listenerCancel := context.WithTimeout(ctx, stateChangeListenerTimeout)
	defer listenerCancel()

	listener.OnTenantStateChange(listenerCtx, tenantID, serviceName, fromState, toState)
}
