package runtime

import (
	"context"

	libobsruntime "github.com/LerianStudio/lib-observability/runtime"
)

// ErrPanic is the sentinel error for recovered panics recorded to spans.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.ErrPanic instead.
var ErrPanic = libobsruntime.ErrPanic

// PanicSpanEventName is the event name used when recording panic events on spans.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.PanicSpanEventName instead.
const PanicSpanEventName = libobsruntime.PanicSpanEventName

// RecordPanicToSpan records a recovered panic as an error event on the current span.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.RecordPanicToSpan instead.
func RecordPanicToSpan(ctx context.Context, panicValue any, stack []byte, goroutineName string) {
	libobsruntime.RecordPanicToSpan(ctx, panicValue, stack, goroutineName)
}

// RecordPanicToSpanWithComponent is like RecordPanicToSpan but also includes the component name.
//
// Deprecated: Use github.com/LerianStudio/lib-observability/runtime.RecordPanicToSpanWithComponent instead.
func RecordPanicToSpanWithComponent(ctx context.Context, panicValue any, stack []byte, component, goroutineName string) {
	libobsruntime.RecordPanicToSpanWithComponent(ctx, panicValue, stack, component, goroutineName)
}
