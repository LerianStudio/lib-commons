// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

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
