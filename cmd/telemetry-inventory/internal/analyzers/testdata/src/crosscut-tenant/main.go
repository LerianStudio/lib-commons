// Package crosscuttenant exercises the tenant-consistency branch of the
// CrossCutAnalyzer: a function that emits a tenant-scoped metric (label
// includes tenant_id) but opens a span without tenant_id attribute.
package crosscuttenant

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// emit pairs a tenant-scoped counter with a span that lacks tenant_id
// attribution. CrossCutAnalyzer must emit a "tenant_consistency" finding
// pointed at the span.
func emit(ctx context.Context, meter metric.Meter, tracer trace.Tracer, tenantID string) {
	counter, _ := meter.Int64Counter("ledger_transactions_total")
	counter.Add(ctx, 1, metric.WithAttributes(attribute.String("tenant_id", tenantID)))

	// Span has no tenant_id attribute — this is the gap the analyzer detects.
	ctx, span := tracer.Start(ctx, "Ledger.Transfer")
	defer span.End()
	_ = ctx
}
