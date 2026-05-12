package metrics

import (
	"context"
)

const metricUnitPercentage = "percentage"

// Pre-configured system metrics for infrastructure monitoring.
var (
	// MetricSystemCPUUsage is a gauge that records the current CPU usage percentage.
	MetricSystemCPUUsage = Metric{
		Name:        "system.cpu.usage",
		Unit:        metricUnitPercentage,
		Description: "Current CPU usage percentage of the process host.",
	}

	// MetricSystemMemUsage is a gauge that records the current memory usage percentage.
	MetricSystemMemUsage = Metric{
		Name:        "system.mem.usage",
		Unit:        metricUnitPercentage,
		Description: "Current memory usage percentage of the process host.",
	}
)

// RecordSystemCPUUsage records the current CPU usage percentage via the factory's gauge.
// The percentage must be in the range [0, 100].
func (f *MetricsFactory) RecordSystemCPUUsage(ctx context.Context, percentage int64) error {
	if f == nil {
		return ErrNilFactory
	}

	if percentage < 0 || percentage > 100 {
		return ErrPercentageOutOfRange
	}

	b, err := f.Gauge(MetricSystemCPUUsage)
	if err != nil {
		return err
	}

	return b.Set(ctx, percentage)
}

// RecordSystemMemUsage records the current memory usage percentage via the factory's gauge.
// The percentage must be in the range [0, 100].
func (f *MetricsFactory) RecordSystemMemUsage(ctx context.Context, percentage int64) error {
	if f == nil {
		return ErrNilFactory
	}

	if percentage < 0 || percentage > 100 {
		return ErrPercentageOutOfRange
	}

	b, err := f.Gauge(MetricSystemMemUsage)
	if err != nil {
		return err
	}

	return b.Set(ctx, percentage)
}
