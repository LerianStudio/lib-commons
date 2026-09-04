package circuitbreaker

import (
	"context"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	constant "github.com/LerianStudio/lib-observability/v4/constants"
)

const (
	executionResultSuccess          = "success"
	executionResultError            = "error"
	executionResultRejectedOpen     = "rejected_open"
	executionResultRejectedHalfOpen = "rejected_half_open"
)

// Counter definitions emitted by the manager.
const (
	stateTransitionMetricName        = "circuit_breaker_state_transitions_total"
	stateTransitionMetricUnit        = "1"
	stateTransitionMetricDescription = "Total number of circuit breaker state transitions"

	executionMetricName        = "circuit_breaker_executions_total"
	executionMetricUnit        = "1"
	executionMetricDescription = "Total number of circuit breaker executions"
)

// breakerMetrics caches the attribute maps for each execution result, so the
// hot path does not rebuild them per emission.
type breakerMetrics struct {
	executionAttrs map[string]map[string]string
}

// WithMetricsRecorder attaches a recorder so the manager emits
// circuit_breaker_state_transitions_total and circuit_breaker_executions_total
// counters automatically. When nil, metrics are silently skipped.
func WithMetricsRecorder(recorder obs.MetricsRecorder) ManagerOption {
	return func(m *manager) {
		m.metricsRecorder = recorder
	}
}

func (m *manager) buildBreakerMetrics(tenantID, serviceName string) breakerMetrics {
	if m.metricsRecorder == nil {
		return breakerMetrics{}
	}

	return breakerMetrics{executionAttrs: map[string]map[string]string{
		executionResultSuccess:          executionAttributes(tenantID, serviceName, executionResultSuccess),
		executionResultError:            executionAttributes(tenantID, serviceName, executionResultError),
		executionResultRejectedOpen:     executionAttributes(tenantID, serviceName, executionResultRejectedOpen),
		executionResultRejectedHalfOpen: executionAttributes(tenantID, serviceName, executionResultRejectedHalfOpen),
	}}
}

func executionAttributes(tenantID, serviceName, result string) map[string]string {
	attrs := map[string]string{
		"service": constant.SanitizeMetricLabel(serviceName),
		"result":  result,
	}

	if tenantID != "" {
		attrs["tenant_hash"] = constant.SanitizeMetricLabel(tenantHashMetricLabel(tenantID))
	}

	return attrs
}

func stateTransitionAttributes(tenantID, serviceName string, from, to State) map[string]string {
	attrs := map[string]string{
		"service":    constant.SanitizeMetricLabel(serviceName),
		"from_state": string(from),
		"to_state":   string(to),
	}

	if tenantID != "" {
		attrs["tenant_hash"] = constant.SanitizeMetricLabel(tenantHashMetricLabel(tenantID))
	}

	return attrs
}

// recordStateTransition increments the state transition counter.
// No-op when metricsRecorder is nil.
func (m *manager) recordStateTransition(tenantID, serviceName string, from, to State) {
	if m.metricsRecorder == nil {
		return
	}

	err := m.metricsRecorder.AddCounter(
		context.Background(),
		stateTransitionMetricName,
		stateTransitionMetricDescription,
		stateTransitionMetricUnit,
		stateTransitionAttributes(tenantID, serviceName, from, to),
		1,
	)
	if err != nil {
		m.logger.Log(context.Background(), obs.LevelWarn, "failed to record state transition metric", "error", err)
	}
}

// recordExecution increments the execution counter.
// No-op when metricsRecorder is nil.
func (m *manager) recordExecution(slot *breakerSlot, result string) {
	if m.metricsRecorder == nil || slot == nil {
		return
	}

	attrs, ok := slot.metrics.executionAttrs[result]
	if !ok {
		attrs = executionAttributes(slot.tenantID, slot.serviceName, result)
	}

	err := m.metricsRecorder.AddCounter(
		context.Background(),
		executionMetricName,
		executionMetricDescription,
		executionMetricUnit,
		attrs,
		1,
	)
	if err != nil {
		m.logger.Log(context.Background(), obs.LevelWarn, "failed to record execution metric", "error", err)
	}
}
