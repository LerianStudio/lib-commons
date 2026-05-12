package spanleak

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

func leak(ctx context.Context, tr trace.Tracer) {
	_, span := tr.Start(ctx, "Leaky.Op") // want `span "Leaky.Op" balanced=false`
	_ = span
}
