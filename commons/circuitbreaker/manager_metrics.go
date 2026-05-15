package circuitbreaker

import (
	"context"

	constant "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
	"go.opentelemetry.io/otel/attribute"
)

const (
	executionResultSuccess          = "success"
	executionResultError            = "error"
	executionResultRejectedOpen     = "rejected_open"
	executionResultRejectedHalfOpen = "rejected_half_open"
)

// stateTransitionMetric defines the counter for circuit breaker state transitions.
var stateTransitionMetric = metrics.Metric{
	Name:        "circuit_breaker_state_transitions_total",
	Unit:        "1",
	Description: "Total number of circuit breaker state transitions",
}

// executionMetric defines the counter for circuit breaker executions.
var executionMetric = metrics.Metric{
	Name:        "circuit_breaker_executions_total",
	Unit:        "1",
	Description: "Total number of circuit breaker executions",
}

type breakerMetrics struct {
	executionAttrs map[string]attribute.Set
}

// WithMetricsFactory attaches a MetricsFactory so the manager emits
// circuit_breaker_state_transitions_total and circuit_breaker_executions_total
// counters automatically. When nil, metrics are silently skipped.
func WithMetricsFactory(f *metrics.MetricsFactory) ManagerOption {
	return func(m *manager) {
		m.metricsFactory = f
	}
}

func (m *manager) initMetricCounters() {
	if m.metricsFactory == nil {
		return
	}

	stateCounter, err := m.metricsFactory.Counter(stateTransitionMetric)
	if err != nil {
		m.logger.Log(context.Background(), log.LevelWarn, "failed to create state transition metric counter", log.Err(err))
	} else {
		m.stateCounter = stateCounter
	}

	execCounter, err := m.metricsFactory.Counter(executionMetric)
	if err != nil {
		m.logger.Log(context.Background(), log.LevelWarn, "failed to create execution metric counter", log.Err(err))
	} else {
		m.execCounter = execCounter
	}
}

func (m *manager) buildBreakerMetrics(tenantID, serviceName string) breakerMetrics {
	if m.execCounter == nil {
		return breakerMetrics{}
	}

	return breakerMetrics{executionAttrs: map[string]attribute.Set{
		executionResultSuccess:          executionAttributeSet(tenantID, serviceName, executionResultSuccess),
		executionResultError:            executionAttributeSet(tenantID, serviceName, executionResultError),
		executionResultRejectedOpen:     executionAttributeSet(tenantID, serviceName, executionResultRejectedOpen),
		executionResultRejectedHalfOpen: executionAttributeSet(tenantID, serviceName, executionResultRejectedHalfOpen),
	}}
}

func executionAttributeSet(tenantID, serviceName, result string) attribute.Set {
	attrs := []attribute.KeyValue{
		attribute.String("service", constant.SanitizeMetricLabel(serviceName)),
		attribute.String("result", result),
	}

	if tenantID != "" {
		attrs = append(attrs, attribute.String("tenant_hash", constant.SanitizeMetricLabel(tenantHashMetricLabel(tenantID))))
	}

	return attribute.NewSet(attrs...)
}

func stateTransitionAttributeSet(tenantID, serviceName string, from, to State) attribute.Set {
	attrs := []attribute.KeyValue{
		attribute.String("service", constant.SanitizeMetricLabel(serviceName)),
		attribute.String("from_state", string(from)),
		attribute.String("to_state", string(to)),
	}

	if tenantID != "" {
		attrs = append(attrs, attribute.String("tenant_hash", constant.SanitizeMetricLabel(tenantHashMetricLabel(tenantID))))
	}

	return attribute.NewSet(attrs...)
}

// recordStateTransition increments the state transition counter.
// No-op when metricsFactory is nil.
func (m *manager) recordStateTransition(tenantID, serviceName string, from, to State) {
	if m.stateCounter == nil {
		return
	}

	attrs := stateTransitionAttributeSet(tenantID, serviceName, from, to)

	err := m.stateCounter.WithAttributeSet(attrs).AddOne(context.Background())
	if err != nil {
		m.logger.Log(context.Background(), log.LevelWarn, "failed to record state transition metric", log.Err(err))
	}
}

// recordExecution increments the execution counter.
// No-op when metricsFactory is nil.
func (m *manager) recordExecution(slot *breakerSlot, result string) {
	if m.execCounter == nil || slot == nil {
		return
	}

	attrs, ok := slot.metrics.executionAttrs[result]
	if !ok {
		attrs = executionAttributeSet(slot.tenantID, slot.serviceName, result)
	}

	err := m.execCounter.WithAttributeSet(attrs).AddOne(context.Background())
	if err != nil {
		m.logger.Log(context.Background(), log.LevelWarn, "failed to record execution metric", log.Err(err))
	}
}
