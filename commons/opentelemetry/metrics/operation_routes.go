package metrics

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
)

// RecordOperationRouteCreated increments the operation-route-created counter.
func (f *MetricsFactory) RecordOperationRouteCreated(ctx context.Context, attributes ...attribute.KeyValue) error {
	if f == nil {
		return ErrNilFactory
	}

	b, err := f.Counter(MetricOperationRoutesCreated)
	if err != nil {
		return err
	}

	return b.WithAttributes(attributes...).AddOne(ctx)
}
