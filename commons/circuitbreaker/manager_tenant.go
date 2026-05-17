package circuitbreaker

import (
	"context"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/sony/gobreaker"
)

// ResetTenant recreates every breaker for a given tenant with its stored Config.
// No-op if the tenant has no registered breakers.
func (m *manager) ResetTenant(tenantID string) {
	if err := validateTenantID(tenantID); err != nil {
		m.logger.Log(
			context.Background(),
			log.LevelWarn,
			"invalid tenant id for ResetTenant; ignoring request",
			log.String("tenant_hash", tenantHashLabel(tenantID)),
			log.Err(err),
		)

		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	resetCount := 0

	for key, slot := range m.slots {
		if key.tenantID != tenantID {
			continue
		}

		settings := m.buildSettings(slot.tenantID, slot.serviceName, slot.config)
		m.slots[key] = &breakerSlot{
			tenantID:    slot.tenantID,
			serviceName: slot.serviceName,
			breaker:     gobreaker.NewCircuitBreaker(settings),
			config:      slot.config,
			metrics:     m.buildBreakerMetrics(slot.tenantID, slot.serviceName),
		}
		resetCount++
	}

	m.logger.Log(
		context.Background(),
		log.LevelInfo,
		"tenant breakers reset",
		log.String("tenant_hash", tenantHashLabel(tenantID)),
		log.Int("reset_count", resetCount),
	)
}

// RemoveTenant drops every breaker for the given tenant entirely. No-op if
// the tenant has no registered breakers.
func (m *manager) RemoveTenant(tenantID string) {
	if err := validateTenantID(tenantID); err != nil {
		m.logger.Log(
			context.Background(),
			log.LevelWarn,
			"invalid tenant id for RemoveTenant; ignoring request",
			log.String("tenant_hash", tenantHashLabel(tenantID)),
			log.Err(err),
		)

		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	removed := 0

	for key := range m.slots {
		if key.tenantID != tenantID {
			continue
		}

		delete(m.slots, key)

		removed++
	}

	m.logger.Log(
		context.Background(),
		log.LevelInfo,
		"removed tenant breakers",
		log.String("tenant_hash", tenantHashLabel(tenantID)),
		log.Int("removed_count", removed),
	)
}

// Inventory returns a snapshot of every (tenantID, serviceName) pair the
// Manager currently holds. The slice is freshly allocated and safe to
// iterate while other goroutines mutate the Manager — but the snapshot may
// be stale by the time the caller reads it.
func (m *manager) Inventory() []TenantBreakerKey {
	m.mu.RLock()
	defer m.mu.RUnlock()

	keys := make([]TenantBreakerKey, 0, len(m.slots))
	for _, slot := range m.slots {
		keys = append(keys, TenantBreakerKey{
			TenantID:    slot.tenantID,
			ServiceName: slot.serviceName,
		})
	}

	return keys
}
