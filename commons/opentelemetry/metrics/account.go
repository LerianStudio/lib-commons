package metrics

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
)

func (f *MetricsFactory) RecordAccountCreated(ctx context.Context, organizationID, ledgerID string, attributes ...attribute.KeyValue) {
	f.Counter(MetricAccountsCreated).
		WithLabels(f.WithLedgerLabels(organizationID, ledgerID)).
		WithAttributes(attributes...).
		AddOne(ctx)
}
