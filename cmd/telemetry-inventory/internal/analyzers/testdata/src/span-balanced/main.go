package spanbalanced

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func ok(ctx context.Context, tr trace.Tracer) {
	ctx, span := tr.Start(ctx, "Balance.Transfer", trace.WithAttributes(attribute.String("tenant_id", "t-1"))) // want `span "Balance.Transfer" balanced=true`
	defer span.End()
	_ = ctx
}
