package runtime

import (
	"context"

	libobsruntime "github.com/LerianStudio/lib-observability/runtime"
)

// ErrPanic is the sentinel error for recovered panics recorded to spans.
var ErrPanic = libobsruntime.ErrPanic

// PanicSpanEventName is the event name used when recording panic events on spans.
const PanicSpanEventName = libobsruntime.PanicSpanEventName

// RecordPanicToSpan records a recovered panic as an error event on the current span.
func RecordPanicToSpan(ctx context.Context, panicValue any, stack []byte, goroutineName string) {
	libobsruntime.RecordPanicToSpan(ctx, panicValue, stack, goroutineName)
}

// RecordPanicToSpanWithComponent is like RecordPanicToSpan but also includes the component name.
func RecordPanicToSpanWithComponent(ctx context.Context, panicValue any, stack []byte, component, goroutineName string) {
	libobsruntime.RecordPanicToSpanWithComponent(ctx, panicValue, stack, component, goroutineName)
}
