// Package crosscuterrorattribution exercises the error-attribution branch
// of the CrossCutAnalyzer: an `if err != nil { ... }` block that calls
// span.RecordError but NOT span.SetStatus and writes no error log.
package crosscuterrorattribution

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/trace"
)

// emit triggers the error-attribution gap: only RecordError is invoked,
// SetStatus and the structured error-log call are missing. CrossCutAnalyzer
// must emit one finding listing both as "missing".
func emit(ctx context.Context, tracer trace.Tracer) error {
	ctx, span := tracer.Start(ctx, "Ledger.Validate")
	defer span.End()

	err := errors.New("boom")
	if err != nil {
		span.RecordError(err)
		// SetStatus omitted on purpose; no logger call either.
		return err
	}

	_ = ctx

	return nil
}
