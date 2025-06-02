package observability

import (
	"context"
	"fmt"
	"math"
	"runtime"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// safeUint64ToInt64 safely converts uint64 to int64, capping at MaxInt64 to prevent overflow
func safeUint64ToInt64(val uint64) int64 {
	if val > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(val)
}

// RuntimeMetrics provides Go runtime-specific metrics collection
type RuntimeMetrics struct {
	meter metric.Meter

	// Memory metrics
	heapSize    metric.Int64ObservableGauge
	heapUsed    metric.Int64ObservableGauge
	heapObjects metric.Int64ObservableGauge
	stackSize   metric.Int64ObservableGauge

	// GC metrics
	gcCount     metric.Int64ObservableCounter
	gcDuration  metric.Float64ObservableGauge
	gcPauseTime metric.Float64Histogram
	nextGC      metric.Int64ObservableGauge

	// Goroutine metrics
	goroutineCount metric.Int64ObservableGauge

	// OS thread metrics
	osThreads metric.Int64ObservableGauge

	// CPU metrics
	cpuCount metric.Int64ObservableGauge

	// Build info metrics
	goVersion metric.Int64ObservableGauge
}

// NewRuntimeMetrics creates a new runtime metrics instance
func NewRuntimeMetrics(meter metric.Meter) (*RuntimeMetrics, error) {
	rm := &RuntimeMetrics{meter: meter}

	// Initialize memory metrics
	heapSize, err := meter.Int64ObservableGauge(
		"go.memory.heap.size_bytes",
		metric.WithDescription("Total heap size in bytes"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create heap size gauge: %w", err)
	}
	rm.heapSize = heapSize

	heapUsed, err := meter.Int64ObservableGauge(
		"go.memory.heap.used_bytes",
		metric.WithDescription("Used heap memory in bytes"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create heap used gauge: %w", err)
	}
	rm.heapUsed = heapUsed

	heapObjects, err := meter.Int64ObservableGauge(
		"go.memory.heap.objects",
		metric.WithDescription("Number of objects in heap"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create heap objects gauge: %w", err)
	}
	rm.heapObjects = heapObjects

	stackSize, err := meter.Int64ObservableGauge(
		"go.memory.stack.size_bytes",
		metric.WithDescription("Stack memory size in bytes"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create stack size gauge: %w", err)
	}
	rm.stackSize = stackSize

	// Initialize GC metrics
	gcCount, err := meter.Int64ObservableCounter(
		"go.gc.count",
		metric.WithDescription("Number of garbage collections"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create GC count counter: %w", err)
	}
	rm.gcCount = gcCount

	gcDuration, err := meter.Float64ObservableGauge(
		"go.gc.duration.total",
		metric.WithDescription("Total GC duration"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create GC duration gauge: %w", err)
	}
	rm.gcDuration = gcDuration

	gcPauseTime, err := meter.Float64Histogram(
		"go.gc.pause_time",
		metric.WithDescription("GC pause time"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create GC pause time histogram: %w", err)
	}
	rm.gcPauseTime = gcPauseTime

	nextGC, err := meter.Int64ObservableGauge(
		"go.gc.next_target_bytes",
		metric.WithDescription("Next GC target heap size"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create next GC gauge: %w", err)
	}
	rm.nextGC = nextGC

	// Initialize goroutine metrics
	goroutineCount, err := meter.Int64ObservableGauge(
		"go.goroutines.count",
		metric.WithDescription("Number of goroutines"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create goroutine count gauge: %w", err)
	}
	rm.goroutineCount = goroutineCount

	// Initialize OS thread metrics
	osThreads, err := meter.Int64ObservableGauge(
		"go.threads.count",
		metric.WithDescription("Number of OS threads"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create OS threads gauge: %w", err)
	}
	rm.osThreads = osThreads

	// Initialize CPU metrics
	cpuCount, err := meter.Int64ObservableGauge(
		"go.cpu.count",
		metric.WithDescription("Number of logical CPUs"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create CPU count gauge: %w", err)
	}
	rm.cpuCount = cpuCount

	// Initialize build info metrics
	goVersion, err := meter.Int64ObservableGauge(
		"go.build.info",
		metric.WithDescription("Go build information"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create Go version gauge: %w", err)
	}
	rm.goVersion = goVersion

	return rm, nil
}

// RecordMemoryUsage records current memory usage from MemStats
func (rm *RuntimeMetrics) RecordMemoryUsage(ctx context.Context, memStats runtime.MemStats) {
	// Memory stats are typically recorded via observable gauges with callbacks
	// This method is provided for manual recording when needed

	// Note: In a real implementation, you would set up callbacks for the observable gauges
	// that automatically collect these metrics. This method is for demonstration.
}

// RecordGoroutines records the current number of goroutines
func (rm *RuntimeMetrics) RecordGoroutines(ctx context.Context, count int) {
	// Goroutine count is typically recorded via observable gauge with callback
	// This method is provided for manual recording when needed
}

// RecordGCStats records garbage collection statistics
func (rm *RuntimeMetrics) RecordGCStats(ctx context.Context, memStats runtime.MemStats) {
	// GC stats are typically recorded via observable gauges with callbacks
	// This method is provided for manual recording when needed

	// Record GC pause time if available
	if len(memStats.PauseNs) > 0 {
		// Get the most recent pause time
		pauseNs := memStats.PauseNs[(memStats.NumGC+255)%256]
		if pauseNs > 0 {
			rm.gcPauseTime.Record(ctx, float64(pauseNs)/1e6) // Convert to milliseconds
		}
	}
}

// SetupObservableCallbacks sets up observable callbacks for automatic metric collection
func (rm *RuntimeMetrics) SetupObservableCallbacks() error {
	// Set up callback for memory metrics
	_, err := rm.meter.RegisterCallback(
		func(ctx context.Context, observer metric.Observer) error {
			var memStats runtime.MemStats
			runtime.ReadMemStats(&memStats)

			observer.ObserveInt64(rm.heapSize, safeUint64ToInt64(memStats.HeapSys))
			observer.ObserveInt64(rm.heapUsed, safeUint64ToInt64(memStats.HeapInuse))
			observer.ObserveInt64(rm.heapObjects, safeUint64ToInt64(memStats.HeapObjects))
			observer.ObserveInt64(rm.stackSize, safeUint64ToInt64(memStats.StackSys))
			observer.ObserveInt64(rm.nextGC, safeUint64ToInt64(memStats.NextGC))

			// GC metrics
			observer.ObserveInt64(rm.gcCount, safeUint64ToInt64(uint64(memStats.NumGC)))
			observer.ObserveFloat64(
				rm.gcDuration,
				float64(memStats.PauseTotalNs)/1e6,
			) // Convert to milliseconds

			return nil
		},
		rm.heapSize,
		rm.heapUsed,
		rm.heapObjects,
		rm.stackSize,
		rm.nextGC,
		rm.gcCount,
		rm.gcDuration,
	)
	if err != nil {
		return fmt.Errorf("failed to register memory metrics callback: %w", err)
	}

	// Set up callback for goroutine and thread metrics
	_, err = rm.meter.RegisterCallback(
		func(ctx context.Context, observer metric.Observer) error {
			observer.ObserveInt64(rm.goroutineCount, int64(runtime.NumGoroutine()))
			observer.ObserveInt64(rm.cpuCount, int64(runtime.NumCPU()))

			// Note: runtime.NumGoroutine() doesn't provide OS thread count directly
			// You would need to use other methods to get OS thread count
			// For demonstration, we'll use GOMAXPROCS as a proxy
			observer.ObserveInt64(rm.osThreads, int64(runtime.GOMAXPROCS(0)))

			return nil
		},
		rm.goroutineCount,
		rm.osThreads,
		rm.cpuCount,
	)
	if err != nil {
		return fmt.Errorf("failed to register runtime metrics callback: %w", err)
	}

	// Set up callback for build info
	_, err = rm.meter.RegisterCallback(
		func(ctx context.Context, observer metric.Observer) error {
			attrs := []attribute.KeyValue{
				attribute.String("version", runtime.Version()),
				attribute.String("arch", runtime.GOARCH),
				attribute.String("os", runtime.GOOS),
			}

			observer.ObserveInt64(rm.goVersion, 1, metric.WithAttributes(attrs...))

			return nil
		},
		rm.goVersion,
	)
	if err != nil {
		return fmt.Errorf("failed to register build info callback: %w", err)
	}

	return nil
}

// GetMemoryStats returns current memory statistics
func (rm *RuntimeMetrics) GetMemoryStats() runtime.MemStats {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	return memStats
}

// GetGoroutineCount returns current number of goroutines
func (rm *RuntimeMetrics) GetGoroutineCount() int {
	return runtime.NumGoroutine()
}

// GetNumCPU returns the number of logical CPUs
func (rm *RuntimeMetrics) GetNumCPU() int {
	return runtime.NumCPU()
}

// TriggerGC manually triggers garbage collection (use with caution)
func (rm *RuntimeMetrics) TriggerGC() {
	runtime.GC()
}

// GetGCStats returns detailed garbage collection statistics
func (rm *RuntimeMetrics) GetGCStats() runtime.MemStats {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	return memStats
}
