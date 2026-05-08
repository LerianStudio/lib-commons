package histogram

import (
	"context"

	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func emitTier1(meter metric.Meter, ctx context.Context) {
	h, _ := meter.Int64Histogram("request_duration_ms", metric.WithExplicitBucketBoundaries(1, 5, 10)) // want `histogram "request_duration_ms" tier=1`
	h.Record(ctx, 10, metric.WithAttributes(attribute.String("tenant_id", "t-1")))                     // want `histogram record site labels=\[tenant_id\]`

	// Float64 branch — covers the second entry in tier1Constructors.
	fh, _ := meter.Float64Histogram("request_duration_seconds")                      // want `histogram "request_duration_seconds" tier=1`
	fh.Record(ctx, 0.5, metric.WithAttributes(attribute.String("tenant_id", "t-1"))) // want `histogram record site labels=\[tenant_id\]`
}

func emitTier2(f *metrics.MetricsFactory, ctx context.Context) error {
	h, err := f.Histogram(metrics.Metric{Name: "db_query_ms", Unit: "ms", Description: "query", Buckets: []float64{1, 10}}) // want `histogram "db_query_ms" tier=2`
	if err != nil {
		return err
	}
	return h.WithAttributes(attribute.String("tenant_id", "t-1")).Record(ctx, 10) // want `histogram record site labels=\[tenant_id\]`
}
