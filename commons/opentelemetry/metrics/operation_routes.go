package metrics

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
)

func (f *MetricsFactory) RecordOperationRouteCreated(ctx context.Context, organizationID, ledgerID string, attributes ...attribute.KeyValue) {
	f.Counter(MetricOperationRoutesCreated).
		WithLabels(f.WithLedgerLabels(organizationID, ledgerID)).
		WithAttributes(attributes...).
		AddOne(ctx)
}
