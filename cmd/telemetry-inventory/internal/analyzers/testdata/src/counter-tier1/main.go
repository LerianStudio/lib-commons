package countertier1

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func emit(meter metric.Meter, ctx context.Context, tenantID string) {
	counter, _ := meter.Int64Counter("ledger_transactions_total")                       // want `counter "ledger_transactions_total" tier=1`
	counter.Add(ctx, 1, metric.WithAttributes(attribute.String("tenant_id", tenantID))) // want `counter record site labels=\[tenant_id\]`

	// Float64 branch — same analyzer code path, different selector name.
	// Ensures tier1Constructors covers both Int64* and Float64* variants.
	fcounter, _ := meter.Float64Counter("ledger_volume_total")                             // want `counter "ledger_volume_total" tier=1`
	fcounter.Add(ctx, 1.5, metric.WithAttributes(attribute.String("tenant_id", tenantID))) // want `counter record site labels=\[tenant_id\]`
}
