package http

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons"
	"github.com/LerianStudio/lib-observability/runtime"
)

// Metrics collector singleton state.
var (
	metricsCollectorOnce     = &sync.Once{}
	metricsCollectorShutdown chan struct{}
	metricsCollectorMu       sync.Mutex
	metricsCollectorStarted  bool
	metricsCollectorInitErr  error
)

// telemetryRuntimeLogger returns the runtime logger from the telemetry middleware, or nil.
func telemetryRuntimeLogger(tm *TelemetryMiddleware) runtime.Logger {
	if tm == nil || tm.Telemetry == nil {
		return nil
	}

	return tm.Telemetry.Logger
}

// collectMetrics ensures the background metrics collector goroutine is running.
func (tm *TelemetryMiddleware) collectMetrics(_ context.Context) error {
	return tm.ensureMetricsCollector()
}

// getMetricsCollectionInterval returns the metrics collection interval.
// Can be configured via METRICS_COLLECTION_INTERVAL environment variable.
// Accepts Go duration format (e.g., "10s", "1m", "500ms").
// Falls back to DefaultMetricsCollectionInterval if not set or invalid.
func getMetricsCollectionInterval() time.Duration {
	if envInterval := os.Getenv("METRICS_COLLECTION_INTERVAL"); envInterval != "" {
		if parsed, err := time.ParseDuration(envInterval); err == nil && parsed > 0 {
			return parsed
		}
	}

	return DefaultMetricsCollectionInterval
}

// ensureMetricsCollector lazily starts the background metrics collector singleton.
func (tm *TelemetryMiddleware) ensureMetricsCollector() error {
	if tm == nil || tm.Telemetry == nil {
		return nil
	}

	if tm.Telemetry.MeterProvider == nil {
		return nil
	}

	metricsCollectorMu.Lock()
	defer metricsCollectorMu.Unlock()

	if metricsCollectorStarted {
		return nil
	}

	if metricsCollectorInitErr != nil {
		metricsCollectorOnce = &sync.Once{}
		metricsCollectorInitErr = nil
	}

	metricsCollectorOnce.Do(func() {
		factory := tm.Telemetry.MetricsFactory
		if factory == nil {
			metricsCollectorInitErr = errors.New("telemetry MetricsFactory is nil, cannot start system metrics collector")
			return
		}

		metricsCollectorShutdown = make(chan struct{})
		ticker := time.NewTicker(getMetricsCollectionInterval())

		runtime.SafeGoWithContextAndComponent(
			context.Background(),
			telemetryRuntimeLogger(tm),
			"http",
			"metrics_collector",
			runtime.KeepRunning,
			func(_ context.Context) {
				commons.GetCPUUsage(context.Background(), factory)
				commons.GetMemUsage(context.Background(), factory)

				for {
					select {
					case <-metricsCollectorShutdown:
						ticker.Stop()
						return
					case <-ticker.C:
						commons.GetCPUUsage(context.Background(), factory)
						commons.GetMemUsage(context.Background(), factory)
					}
				}
			},
		)

		metricsCollectorStarted = true
	})

	return metricsCollectorInitErr
}

// StopMetricsCollector stops the background metrics collector goroutine.
// Should be called during application shutdown for graceful cleanup.
// After calling this function, the collector can be restarted by new requests.
//
// Implementation note: This function intentionally resets sync.Once to a new instance
// to allow the collector to be restarted after being stopped. This is an unusual but
// intentional pattern - the mutex ensures thread-safety during the reset operation,
// preventing race conditions between Stop and subsequent Start calls.
func StopMetricsCollector() {
	metricsCollectorMu.Lock()
	defer metricsCollectorMu.Unlock()

	if metricsCollectorStarted && metricsCollectorShutdown != nil {
		close(metricsCollectorShutdown)

		metricsCollectorStarted = false
		metricsCollectorOnce = &sync.Once{}
		metricsCollectorInitErr = nil
	}
}
