package opentelemetry

import (
	"context"

	"github.com/LerianStudio/lib-commons/v2/commons"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// ---- SpanProcessor that applies the AttrBag to every new span ----

// AttrBagSpanProcessor copies request-scoped attributes from context into every span at start.
type AttrBagSpanProcessor struct{}

func (AttrBagSpanProcessor) OnStart(ctx context.Context, s sdktrace.ReadWriteSpan) {
	if kv := commons.AttributesFromContext(ctx); len(kv) > 0 {
		s.SetAttributes(kv...)
	}
}

func (AttrBagSpanProcessor) OnEnd(s sdktrace.ReadOnlySpan) {}

func (AttrBagSpanProcessor) Shutdown(ctx context.Context) error { return nil }

func (AttrBagSpanProcessor) ForceFlush(ctx context.Context) error { return nil }
