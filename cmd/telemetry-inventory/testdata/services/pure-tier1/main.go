package main

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func emit(meter metric.Meter, ctx context.Context) {
	counter, _ := meter.Int64Counter("ledger_transactions_total")
	counter.Add(ctx, 1, metric.WithAttributes(attribute.String("tenant_id", "t-1")))
}

func main() {}
