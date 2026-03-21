package opentelemetry

import (
	"context"
	"strings"

	"github.com/LerianStudio/lib-commons/v4/commons"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// ---- SpanProcessor that applies the AttrBag to every new span ----

// AttrBagSpanProcessor copies request-scoped attributes from context into every span at start.
type AttrBagSpanProcessor struct{}

// RedactingAttrBagSpanProcessor copies request attributes and applies redaction rules by key.
type RedactingAttrBagSpanProcessor struct {
	Redactor *Redactor
}

// OnStart applies request-scoped context attributes to newly started spans.
func (AttrBagSpanProcessor) OnStart(ctx context.Context, s sdktrace.ReadWriteSpan) {
	if kv := commons.AttributesFromContext(ctx); len(kv) > 0 {
		s.SetAttributes(kv...)
	}
}

// OnStart applies request-scoped attributes and redacts sensitive values before writing to span.
func (p RedactingAttrBagSpanProcessor) OnStart(ctx context.Context, s sdktrace.ReadWriteSpan) {
	kv := commons.AttributesFromContext(ctx)
	if len(kv) == 0 {
		return
	}

	if p.Redactor != nil {
		kv = redactAttributesByKey(kv, p.Redactor)
	}

	s.SetAttributes(kv...)
}

// OnEnd is a no-op for this processor.
func (AttrBagSpanProcessor) OnEnd(sdktrace.ReadOnlySpan) {}

// OnEnd is a no-op for this processor.
func (RedactingAttrBagSpanProcessor) OnEnd(sdktrace.ReadOnlySpan) {}

// Shutdown is a no-op and always returns nil.
func (AttrBagSpanProcessor) Shutdown(context.Context) error { return nil }

// Shutdown is a no-op and always returns nil.
func (RedactingAttrBagSpanProcessor) Shutdown(context.Context) error { return nil }

// ForceFlush is a no-op and always returns nil.
func (AttrBagSpanProcessor) ForceFlush(context.Context) error { return nil }

// ForceFlush is a no-op and always returns nil.
func (RedactingAttrBagSpanProcessor) ForceFlush(context.Context) error { return nil }

func redactAttributesByKey(attrs []attribute.KeyValue, redactor *Redactor) []attribute.KeyValue {
	if redactor == nil {
		return attrs
	}

	redacted := make([]attribute.KeyValue, 0, len(attrs))
	for _, attr := range attrs {
		key := string(attr.Key)

		fieldName := key
		if idx := strings.LastIndex(key, "."); idx >= 0 && idx+1 < len(key) {
			fieldName = key[idx+1:]
		}

		action, ok := redactor.actionFor(key, fieldName)
		if !ok {
			redacted = append(redacted, attr)
			continue
		}

		switch action {
		case RedactionDrop:
			continue
		case RedactionHash:
			redacted = append(redacted, attribute.String(string(attr.Key), redactor.hashString(attr.Value.Emit())))
		default:
			redacted = append(redacted, attribute.String(string(attr.Key), redactor.maskValue))
		}
	}

	return redacted
}
