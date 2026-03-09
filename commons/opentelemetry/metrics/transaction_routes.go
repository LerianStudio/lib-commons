package metrics

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
)

// RecordTransactionRouteCreated increments the transaction-route-created counter.
func (f *MetricsFactory) RecordTransactionRouteCreated(ctx context.Context, attributes ...attribute.KeyValue) error {
	if f == nil {
		return ErrNilFactory
	}

	b, err := f.Counter(MetricTransactionRoutesCreated)
	if err != nil {
		return err
	}

	return b.WithAttributes(attributes...).AddOne(ctx)
}
