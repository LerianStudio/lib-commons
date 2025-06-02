package circuitbreaker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// PrometheusMetrics contains Prometheus metrics for circuit breakers
type PrometheusMetrics struct {
	// Counter metrics
	requestsTotal    *prometheus.CounterVec
	successesTotal   *prometheus.CounterVec
	failuresTotal    *prometheus.CounterVec
	rejectionsTotal  *prometheus.CounterVec
	stateTransitions *prometheus.CounterVec

	// Gauge metrics
	currentState        *prometheus.GaugeVec
	consecutiveFailures *prometheus.GaugeVec

	// Histogram metrics
	operationDuration *prometheus.HistogramVec

	// Summary metrics for percentiles
	operationSummary *prometheus.SummaryVec
}

// MetricsExporter handles Prometheus metrics export for circuit breakers
type MetricsExporter struct {
	namespace string
	subsystem string
	metrics   *PrometheusMetrics
	registry  prometheus.Registerer
	mu        sync.RWMutex
	enabled   bool
}

// MetricsConfig configures metrics export
type MetricsConfig struct {
	Namespace string
	Subsystem string
	Enabled   bool
	Registry  prometheus.Registerer
	Labels    map[string]string
}

// DefaultMetricsConfig returns default metrics configuration
func DefaultMetricsConfig() MetricsConfig {
	return MetricsConfig{
		Namespace: "circuitbreaker",
		Subsystem: "",
		Enabled:   true,
		Registry:  prometheus.DefaultRegisterer,
		Labels:    make(map[string]string),
	}
}

// NewMetricsExporter creates a new Prometheus metrics exporter
func NewMetricsExporter(config MetricsConfig) *MetricsExporter {
	if config.Registry == nil {
		config.Registry = prometheus.DefaultRegisterer
	}

	exporter := &MetricsExporter{
		namespace: config.Namespace,
		subsystem: config.Subsystem,
		registry:  config.Registry,
		enabled:   config.Enabled,
	}

	if config.Enabled {
		exporter.initializeMetrics(config.Labels)
	}

	return exporter
}

// initializeMetrics sets up all Prometheus metrics
func (me *MetricsExporter) initializeMetrics(labels map[string]string) {
	baseLabels := []string{"circuit_breaker_name", "service"}
	for key := range labels {
		baseLabels = append(baseLabels, key)
	}

	me.metrics = &PrometheusMetrics{
		// Counter metrics
		requestsTotal: promauto.With(me.registry).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: me.namespace,
				Subsystem: me.subsystem,
				Name:      "requests_total",
				Help:      "Total number of requests processed by circuit breaker",
			},
			baseLabels,
		),

		successesTotal: promauto.With(me.registry).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: me.namespace,
				Subsystem: me.subsystem,
				Name:      "successes_total",
				Help:      "Total number of successful requests",
			},
			baseLabels,
		),

		failuresTotal: promauto.With(me.registry).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: me.namespace,
				Subsystem: me.subsystem,
				Name:      "failures_total",
				Help:      "Total number of failed requests",
			},
			append(baseLabels, "failure_reason"),
		),

		rejectionsTotal: promauto.With(me.registry).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: me.namespace,
				Subsystem: me.subsystem,
				Name:      "rejections_total",
				Help:      "Total number of rejected requests due to open circuit",
			},
			baseLabels,
		),

		stateTransitions: promauto.With(me.registry).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: me.namespace,
				Subsystem: me.subsystem,
				Name:      "state_transitions_total",
				Help:      "Total number of state transitions",
			},
			append(baseLabels, "from_state", "to_state"),
		),

		// Gauge metrics
		currentState: promauto.With(me.registry).NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: me.namespace,
				Subsystem: me.subsystem,
				Name:      "state",
				Help:      "Current state of circuit breaker (0=closed, 1=open, 2=half-open)",
			},
			baseLabels,
		),

		consecutiveFailures: promauto.With(me.registry).NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: me.namespace,
				Subsystem: me.subsystem,
				Name:      "consecutive_failures",
				Help:      "Current number of consecutive failures",
			},
			baseLabels,
		),

		// Histogram metrics
		operationDuration: promauto.With(me.registry).NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: me.namespace,
				Subsystem: me.subsystem,
				Name:      "operation_duration_seconds",
				Help:      "Duration of operations in seconds",
				Buckets:   prometheus.ExponentialBuckets(0.001, 2, 15), // 1ms to ~32s
			},
			append(baseLabels, "result"),
		),

		// Summary metrics for percentiles
		operationSummary: promauto.With(me.registry).NewSummaryVec(
			prometheus.SummaryOpts{
				Namespace: me.namespace,
				Subsystem: me.subsystem,
				Name:      "operation_duration_summary_seconds",
				Help:      "Summary of operation durations in seconds",
				Objectives: map[float64]float64{
					0.50: 0.05,  // 50th percentile with 5% tolerance
					0.90: 0.01,  // 90th percentile with 1% tolerance
					0.95: 0.005, // 95th percentile with 0.5% tolerance
					0.99: 0.001, // 99th percentile with 0.1% tolerance
				},
				MaxAge: 10 * time.Minute,
			},
			append(baseLabels, "result"),
		),
	}
}

// RecordRequest records a request attempt
func (me *MetricsExporter) RecordRequest(name, service string, labels map[string]string) {
	if !me.enabled || me.metrics == nil {
		return
	}

	labelValues := me.buildLabelValues(name, service, labels)
	me.metrics.requestsTotal.WithLabelValues(labelValues...).Inc()
}

// RecordSuccess records a successful operation
func (me *MetricsExporter) RecordSuccess(
	name, service string,
	duration time.Duration,
	labels map[string]string,
) {
	if !me.enabled || me.metrics == nil {
		return
	}

	labelValues := me.buildLabelValues(name, service, labels)

	// Increment success counter
	me.metrics.successesTotal.WithLabelValues(labelValues...).Inc()

	// Record duration
	durationSeconds := duration.Seconds()
	resultLabelValues := append(labelValues, "success")
	me.metrics.operationDuration.WithLabelValues(resultLabelValues...).Observe(durationSeconds)
	me.metrics.operationSummary.WithLabelValues(resultLabelValues...).Observe(durationSeconds)
}

// RecordFailure records a failed operation
func (me *MetricsExporter) RecordFailure(
	name, service string,
	duration time.Duration,
	failureReason string,
	labels map[string]string,
) {
	if !me.enabled || me.metrics == nil {
		return
	}

	labelValues := me.buildLabelValues(name, service, labels)

	// Increment failure counter with reason
	failureLabelValues := append(labelValues, failureReason)
	me.metrics.failuresTotal.WithLabelValues(failureLabelValues...).Inc()

	// Record duration
	durationSeconds := duration.Seconds()
	resultLabelValues := append(labelValues, "failure")
	me.metrics.operationDuration.WithLabelValues(resultLabelValues...).Observe(durationSeconds)
	me.metrics.operationSummary.WithLabelValues(resultLabelValues...).Observe(durationSeconds)
}

// RecordRejection records a rejected request due to open circuit
func (me *MetricsExporter) RecordRejection(name, service string, labels map[string]string) {
	if !me.enabled || me.metrics == nil {
		return
	}

	labelValues := me.buildLabelValues(name, service, labels)
	me.metrics.rejectionsTotal.WithLabelValues(labelValues...).Inc()
}

// RecordStateTransition records a state change
func (me *MetricsExporter) RecordStateTransition(
	name, service string,
	fromState, toState State,
	labels map[string]string,
) {
	if !me.enabled || me.metrics == nil {
		return
	}

	labelValues := me.buildLabelValues(name, service, labels)

	// Record state transition
	transitionLabelValues := append(labelValues, fromState.String(), toState.String())
	me.metrics.stateTransitions.WithLabelValues(transitionLabelValues...).Inc()

	// Update current state gauge
	me.UpdateCurrentState(name, service, toState, labels)
}

// UpdateCurrentState updates the current state gauge
func (me *MetricsExporter) UpdateCurrentState(
	name, service string,
	state State,
	labels map[string]string,
) {
	if !me.enabled || me.metrics == nil {
		return
	}

	labelValues := me.buildLabelValues(name, service, labels)

	var stateValue float64
	switch state {
	case StateClosed:
		stateValue = 0
	case StateOpen:
		stateValue = 1
	case StateHalfOpen:
		stateValue = 2
	}

	me.metrics.currentState.WithLabelValues(labelValues...).Set(stateValue)
}

// UpdateConsecutiveFailures updates the consecutive failures gauge
func (me *MetricsExporter) UpdateConsecutiveFailures(
	name, service string,
	count int64,
	labels map[string]string,
) {
	if !me.enabled || me.metrics == nil {
		return
	}

	labelValues := me.buildLabelValues(name, service, labels)
	me.metrics.consecutiveFailures.WithLabelValues(labelValues...).Set(float64(count))
}

// buildLabelValues constructs label values for metrics
func (me *MetricsExporter) buildLabelValues(
	name, service string,
	labels map[string]string,
) []string {
	labelValues := []string{name, service}

	// Add custom labels in consistent order
	for key, value := range labels {
		labelValues = append(labelValues, value)
		_ = key // Use key if needed for ordering
	}

	return labelValues
}

// GetMetricsRegistry returns the Prometheus registry used by this exporter
func (me *MetricsExporter) GetMetricsRegistry() prometheus.Registerer {
	return me.registry
}

// IsEnabled returns whether metrics collection is enabled
func (me *MetricsExporter) IsEnabled() bool {
	me.mu.RLock()
	defer me.mu.RUnlock()
	return me.enabled
}

// Enable enables metrics collection
func (me *MetricsExporter) Enable() {
	me.mu.Lock()
	defer me.mu.Unlock()
	me.enabled = true
}

// Disable disables metrics collection
func (me *MetricsExporter) Disable() {
	me.mu.Lock()
	defer me.mu.Unlock()
	me.enabled = false
}

// GetCurrentMetrics returns current metric values for a circuit breaker
func (me *MetricsExporter) GetCurrentMetrics(
	name, service string,
	labels map[string]string,
) (*CircuitBreakerMetrics, error) {
	if !me.enabled || me.metrics == nil {
		return nil, fmt.Errorf("metrics not enabled")
	}

	// This would typically query the metrics from the Prometheus registry
	// For simplicity, we return a placeholder implementation
	// In a real implementation, you'd use prometheus.GathererFunc or similar

	return &CircuitBreakerMetrics{
		// These would be actual values from Prometheus
		Name: name,
		// Other fields would be populated from metric queries
	}, nil
}

// CircuitBreakerMetrics contains comprehensive metrics for a circuit breaker
type CircuitBreakerMetrics struct {
	Name                string            `json:"name"`
	Service             string            `json:"service"`
	CurrentState        State             `json:"current_state"`
	Requests            int64             `json:"requests"`
	Successes           int64             `json:"successes"`
	Failures            int64             `json:"failures"`
	Rejections          int64             `json:"rejections"`
	ConsecutiveFailures int64             `json:"consecutive_failures"`
	StateTransitions    map[string]int64  `json:"state_transitions"`
	LastFailureTime     time.Time         `json:"last_failure_time"`
	LastSuccessTime     time.Time         `json:"last_success_time"`
	SuccessRate         float64           `json:"success_rate"`
	FailureRate         float64           `json:"failure_rate"`
	AverageResponseTime time.Duration     `json:"average_response_time"`
	P50ResponseTime     time.Duration     `json:"p50_response_time"`
	P95ResponseTime     time.Duration     `json:"p95_response_time"`
	P99ResponseTime     time.Duration     `json:"p99_response_time"`
	Labels              map[string]string `json:"labels"`
}

// CalculateRates calculates success and failure rates
func (cbm *CircuitBreakerMetrics) CalculateRates() {
	if cbm.Requests > 0 {
		cbm.SuccessRate = float64(cbm.Successes) / float64(cbm.Requests) * 100
		cbm.FailureRate = float64(cbm.Failures) / float64(cbm.Requests) * 100
	}
}

// MetricsSnapshot represents a point-in-time view of circuit breaker metrics
type MetricsSnapshot struct {
	Timestamp       time.Time                         `json:"timestamp"`
	CircuitBreakers map[string]*CircuitBreakerMetrics `json:"circuit_breakers"`
	Summary         *MetricsSummary                   `json:"summary"`
}

// MetricsSummary provides aggregated metrics across all circuit breakers
type MetricsSummary struct {
	TotalCircuitBreakers int64   `json:"total_circuit_breakers"`
	OpenCircuitBreakers  int64   `json:"open_circuit_breakers"`
	TotalRequests        int64   `json:"total_requests"`
	TotalSuccesses       int64   `json:"total_successes"`
	TotalFailures        int64   `json:"total_failures"`
	TotalRejections      int64   `json:"total_rejections"`
	OverallSuccessRate   float64 `json:"overall_success_rate"`
	OverallFailureRate   float64 `json:"overall_failure_rate"`
}

// MetricsCollector collects metrics from multiple circuit breakers
type MetricsCollector struct {
	exporters map[string]*MetricsExporter
	mu        sync.RWMutex
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		exporters: make(map[string]*MetricsExporter),
	}
}

// RegisterExporter registers a metrics exporter for a service
func (mc *MetricsCollector) RegisterExporter(service string, exporter *MetricsExporter) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.exporters[service] = exporter
}

// UnregisterExporter removes a metrics exporter
func (mc *MetricsCollector) UnregisterExporter(service string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	delete(mc.exporters, service)
}

// GetSnapshot returns a snapshot of all metrics
func (mc *MetricsCollector) GetSnapshot(ctx context.Context) (*MetricsSnapshot, error) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	snapshot := &MetricsSnapshot{
		Timestamp:       time.Now(),
		CircuitBreakers: make(map[string]*CircuitBreakerMetrics),
		Summary:         &MetricsSummary{},
	}

	// Collect metrics from all exporters
	for service, exporter := range mc.exporters {
		if exporter.IsEnabled() {
			// In a real implementation, this would query the actual metrics
			// For now, we'll create placeholder data
			cbMetrics := &CircuitBreakerMetrics{
				Service: service,
				// Other fields would be populated from actual metrics
			}
			cbMetrics.CalculateRates()
			snapshot.CircuitBreakers[service] = cbMetrics
		}
	}

	// Calculate summary
	mc.calculateSummary(snapshot)

	return snapshot, nil
}

// calculateSummary calculates aggregated summary metrics
func (mc *MetricsCollector) calculateSummary(snapshot *MetricsSnapshot) {
	summary := snapshot.Summary
	summary.TotalCircuitBreakers = int64(len(snapshot.CircuitBreakers))

	var totalRequests, totalSuccesses, totalFailures, totalRejections int64
	var openCount int64

	for _, cbMetrics := range snapshot.CircuitBreakers {
		if cbMetrics.CurrentState == StateOpen {
			openCount++
		}

		totalRequests += cbMetrics.Requests
		totalSuccesses += cbMetrics.Successes
		totalFailures += cbMetrics.Failures
		totalRejections += cbMetrics.Rejections
	}

	summary.OpenCircuitBreakers = openCount
	summary.TotalRequests = totalRequests
	summary.TotalSuccesses = totalSuccesses
	summary.TotalFailures = totalFailures
	summary.TotalRejections = totalRejections

	if totalRequests > 0 {
		summary.OverallSuccessRate = float64(totalSuccesses) / float64(totalRequests) * 100
		summary.OverallFailureRate = float64(totalFailures) / float64(totalRequests) * 100
	}
}

// ExportMetrics exports metrics in various formats
func (mc *MetricsCollector) ExportMetrics(ctx context.Context, format string) ([]byte, error) {
	snapshot, err := mc.GetSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get metrics snapshot: %w", err)
	}

	switch format {
	case "json":
		return mc.exportJSON(snapshot)
	case "prometheus":
		return mc.exportPrometheus(snapshot)
	default:
		return nil, fmt.Errorf("unsupported export format: %s", format)
	}
}

func (mc *MetricsCollector) exportJSON(snapshot *MetricsSnapshot) ([]byte, error) {
	// JSON export implementation
	return []byte("{}"), nil // Placeholder
}

func (mc *MetricsCollector) exportPrometheus(snapshot *MetricsSnapshot) ([]byte, error) {
	// Prometheus format export implementation
	return []byte(""), nil // Placeholder
}

// Global metrics collector instance
var globalMetricsCollector = NewMetricsCollector()

// GetGlobalMetricsCollector returns the global metrics collector
func GetGlobalMetricsCollector() *MetricsCollector {
	return globalMetricsCollector
}
