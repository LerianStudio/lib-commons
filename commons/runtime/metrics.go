package runtime

import (
	"context"
	"sync"

	constant "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
)

// PanicMetrics provides panic-related metrics using OpenTelemetry.
// It wraps lib-commons' MetricsFactory for consistent metric handling.
type PanicMetrics struct {
	factory *metrics.MetricsFactory
	logger  Logger
}

// panicRecoveredMetric defines the metric for counting recovered panics.
var panicRecoveredMetric = metrics.Metric{
	Name:        constant.MetricPanicRecoveredTotal,
	Unit:        "1",
	Description: "Total number of recovered panics",
}

// panicMetricsInstance is the singleton instance for panic metrics.
// It is initialized lazily via InitPanicMetrics.
var (
	panicMetricsInstance *PanicMetrics
	panicMetricsMu       sync.RWMutex
)

// InitPanicMetrics initializes panic metrics with the provided MetricsFactory.
//
// Backward compatibility:
//   - InitPanicMetrics(factory)
//   - InitPanicMetrics(factory, logger)
//
// The logger is optional and used only for metric recording diagnostics.
// This should be called once during application startup after telemetry is initialized.
// It is safe to call multiple times; subsequent calls are no-ops.
//
// Example:
//
//	tl, err := opentelemetry.NewTelemetry(cfg)
//	if err != nil {
//	    log.Fatalf("failed to init telemetry: %v", err)
//	}
//	tl.ApplyGlobals()
//	runtime.InitPanicMetrics(tl.MetricsFactory)
func InitPanicMetrics(factory *metrics.MetricsFactory, logger ...Logger) {
	panicMetricsMu.Lock()
	defer panicMetricsMu.Unlock()

	if factory == nil {
		return
	}

	if panicMetricsInstance != nil {
		return // Already initialized
	}

	var l Logger
	if len(logger) > 0 {
		l = logger[0]
	}

	panicMetricsInstance = &PanicMetrics{
		factory: factory,
		logger:  l,
	}
}

// GetPanicMetrics returns the singleton PanicMetrics instance.
// Returns nil if InitPanicMetrics has not been called.
func GetPanicMetrics() *PanicMetrics {
	panicMetricsMu.RLock()
	defer panicMetricsMu.RUnlock()

	return panicMetricsInstance
}

// ResetPanicMetrics clears the panic metrics singleton.
// This is primarily intended for testing to ensure test isolation.
// In production, this should generally not be called.
func ResetPanicMetrics() {
	panicMetricsMu.Lock()
	defer panicMetricsMu.Unlock()

	panicMetricsInstance = nil
}

// RecordPanicRecovered increments the panic_recovered_total counter with the given labels.
// If metrics are not initialized, this is a no-op.
//
// Parameters:
//   - ctx: Context for metric recording (may contain trace correlation)
//   - component: The component where the panic occurred (e.g., "transaction", "onboarding", "crm")
//   - goroutineName: The name of the goroutine or handler (e.g., "http_handler", "rabbitmq_worker")
func (pm *PanicMetrics) RecordPanicRecovered(ctx context.Context, component, goroutineName string) {
	if pm == nil || pm.factory == nil {
		return
	}

	counter, err := pm.factory.Counter(panicRecoveredMetric)
	if err != nil {
		if pm.logger != nil {
			pm.logger.Log(ctx, log.LevelWarn, "failed to create panic metric counter", log.Err(err))
		}

		return
	}

	err = counter.
		WithLabels(map[string]string{
			"component":      constant.SanitizeMetricLabel(component),
			"goroutine_name": constant.SanitizeMetricLabel(goroutineName),
		}).
		AddOne(ctx)
	if err != nil {
		if pm.logger != nil {
			pm.logger.Log(ctx, log.LevelWarn, "failed to record panic metric", log.Err(err))
		}

		return
	}
}

// recordPanicMetric is a package-level helper that records a panic metric if metrics are initialized.
// This is called internally by recovery functions.
func recordPanicMetric(ctx context.Context, component, goroutineName string) {
	pm := GetPanicMetrics()
	if pm != nil {
		pm.RecordPanicRecovered(ctx, component, goroutineName)
	}
}
