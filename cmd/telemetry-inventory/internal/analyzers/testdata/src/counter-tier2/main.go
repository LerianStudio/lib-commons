package countertier2

import (
	"context"

	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
	"go.opentelemetry.io/otel/attribute"
)

func emit(f *metrics.MetricsFactory, ctx context.Context, tenantID string) error {
	b, err := f.Counter(metrics.Metric{Name: "ledger_tx_total", Unit: "1", Description: "demo"}) // want `counter "ledger_tx_total" tier=2`
	if err != nil {
		return err
	}
	return b.WithAttributes(attribute.String("tenant_id", tenantID)).AddOne(ctx) // want `counter record site labels=\[tenant_id\]`
}
