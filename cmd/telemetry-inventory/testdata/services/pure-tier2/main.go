package main

import (
	"context"

	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
	"go.opentelemetry.io/otel/attribute"
)

func emit(f *metrics.MetricsFactory, ctx context.Context) error {
	counter, err := f.Counter(metrics.Metric{Name: "ledger_tx_total", Unit: "1", Description: "ledger transactions"})
	if err != nil {
		return err
	}
	if err := counter.WithAttributes(attribute.String("tenant_id", "t-1")).AddOne(ctx); err != nil {
		return err
	}
	histogram, err := f.Histogram(metrics.Metric{Name: "ledger_tx_duration_ms", Unit: "ms", Description: "ledger latency", Buckets: []float64{1, 5, 10}})
	if err != nil {
		return err
	}
	return histogram.WithAttributes(attribute.String("tenant_id", "t-1")).Record(ctx, 5)
}

func main() {}
