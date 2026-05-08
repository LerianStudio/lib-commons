package countertier3

import (
	"context"

	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
)

func emit(f *metrics.MetricsFactory, ctx context.Context) error {
	if err := f.RecordAccountCreated(ctx); err != nil { // want `counter "accounts_created" tier=3`
		return err
	}
	if err := f.RecordTransactionProcessed(ctx); err != nil { // want `counter "transactions_processed" tier=3`
		return err
	}
	if err := f.RecordTransactionRouteCreated(ctx); err != nil { // want `counter "transaction_routes_created" tier=3`
		return err
	}
	return f.RecordOperationRouteCreated(ctx) // want `counter "operation_routes_created" tier=3`
}
