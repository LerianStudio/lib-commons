// Package crosscuttrace exercises the trace-correlation branch of the
// CrossCutAnalyzer: a function that records a histogram (and emits a log)
// without opening a span in the same function.
package crosscuttrace

import (
	"context"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// emit records a histogram with no enclosing span and writes a structured
// log line — CrossCutAnalyzer must emit a "trace_correlation" finding for
// at least the histogram (logs without spans land in the same bucket).
func emit(ctx context.Context, meter metric.Meter, logger log.Logger) {
	histogram, _ := meter.Int64Histogram("ledger_tx_duration_ms")
	histogram.Record(ctx, 5, metric.WithAttributes(attribute.String("tenant_id", "t-1")))

	logger.Log(ctx, log.LevelInfo, "ledger transfer complete",
		log.String("operation", "transfer"))
}
