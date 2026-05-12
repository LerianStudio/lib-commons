package gauge

import (
	"context"

	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func emitTier1(meter metric.Meter, ctx context.Context) {
	g, _ := meter.Int64Gauge("active_workers")                                    // want `gauge "active_workers" tier=1`
	g.Record(ctx, 5, metric.WithAttributes(attribute.String("tenant_id", "t-1"))) // want `gauge record site labels=\[tenant_id\]`

	// Float64 branch — covers the second entry in tier1Constructors.
	fg, _ := meter.Float64Gauge("cpu_usage_ratio")                                    // want `gauge "cpu_usage_ratio" tier=1`
	fg.Record(ctx, 0.42, metric.WithAttributes(attribute.String("tenant_id", "t-1"))) // want `gauge record site labels=\[tenant_id\]`
}

func emitTier3Mem(f *metrics.MetricsFactory, ctx context.Context) error {
	return f.RecordSystemMemUsage(ctx, 60) // want `gauge "system.mem.usage" tier=3`
}

func emitTier2(f *metrics.MetricsFactory, ctx context.Context) error {
	g, err := f.Gauge(metrics.Metric{Name: "queue_depth", Unit: "1"}) // want `gauge "queue_depth" tier=2`
	if err != nil {
		return err
	}
	return g.WithAttributes(attribute.String("tenant_id", "t-1")).Set(ctx, 12) // want `gauge record site labels=\[tenant_id\]`
}

func emitTier3(f *metrics.MetricsFactory, ctx context.Context) error {
	return f.RecordSystemCPUUsage(ctx, 30) // want `gauge "system.cpu.usage" tier=3`
}
