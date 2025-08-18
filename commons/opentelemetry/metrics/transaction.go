package metrics

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
)

func (f *MetricsFactory) RecordTransactionProcessed(ctx context.Context, organizationID, ledgerID string, attributes ...attribute.KeyValue) {
	f.Counter(MetricTransactionsProcessed).
		WithLabels(f.WithLedgerLabels(organizationID, ledgerID)).
		WithAttributes(attributes...).
		AddOne(ctx)
}
