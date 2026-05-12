package runtime

import (
	"context"
	"fmt"
	"reflect"
	"sync"
)

// ErrorReporter defines an interface for external error reporting services.
// This abstraction allows integration with error tracking services (e.g., logging
// to Grafana Loki, sending to an alerting system) without creating a hard
// dependency on any specific SDK.
//
// Implementations should:
//   - Handle nil contexts gracefully
//   - Be safe for concurrent use
//   - Not panic themselves
type ErrorReporter interface {
	// CaptureException reports a panic/exception to the error tracking service.
	// The tags map can include metadata like "component", "goroutine_name", etc.
	CaptureException(ctx context.Context, err error, tags map[string]string)
}

// errorReporterInstance is the singleton error reporter.
// It remains nil unless explicitly configured.
var (
	errorReporterInstance ErrorReporter
	errorReporterMu       sync.RWMutex
)

// SetErrorReporter configures the global error reporter for panic reporting.
// Pass nil to disable error reporting.
//
// This should be called once during application startup if an external
// error tracking service is desired.
//
// Example with structured logging:
//
//	type logReporter struct {
//	    logger *slog.Logger
//	}
//
//	func (r *logReporter) CaptureException(ctx context.Context, err error, tags map[string]string) {
//	    attrs := make([]any, 0, len(tags)*2)
//	    for k, v := range tags {
//	        attrs = append(attrs, k, v)
//	    }
//	    r.logger.ErrorContext(ctx, "panic recovered", append(attrs, "error", err)...)
//	}
//
//	runtime.SetErrorReporter(&logReporter{logger: slog.Default()})
func SetErrorReporter(reporter ErrorReporter) {
	errorReporterMu.Lock()
	defer errorReporterMu.Unlock()

	errorReporterInstance = reporter
}

// GetErrorReporter returns the currently configured error reporter.
// Returns nil if no reporter has been configured.
func GetErrorReporter() ErrorReporter {
	errorReporterMu.RLock()
	defer errorReporterMu.RUnlock()

	return errorReporterInstance
}

var (
	// productionMode controls whether sensitive data is redacted in error reports.
	// When true, stack traces and detailed panic values are suppressed.
	productionMode   bool
	productionModeMu sync.RWMutex
)

const redactedPanicMsg = "panic recovered (details redacted)"

// SetProductionMode enables or disables production mode for error reporting.
// In production mode, stack traces and potentially sensitive panic details are redacted.
func SetProductionMode(enabled bool) {
	productionModeMu.Lock()
	defer productionModeMu.Unlock()

	productionMode = enabled
}

// IsProductionMode returns whether production mode is enabled.
func IsProductionMode() bool {
	productionModeMu.RLock()
	defer productionModeMu.RUnlock()

	return productionMode
}

// reportPanicToErrorService reports a panic to the configured error reporter if one exists.
// This is called internally by recovery functions.
// In production mode, stack traces and potentially sensitive panic values are redacted.
func reportPanicToErrorService(
	ctx context.Context,
	panicValue any,
	stack []byte,
	component, goroutineName string,
) {
	reporter := GetErrorReporter()
	if reporter == nil {
		return
	}

	isProduction := IsProductionMode()

	// Convert panic value to error, redacting details in production
	err := toPanicError(panicValue, isProduction)

	tags := map[string]string{
		"component":      component,
		"goroutine_name": goroutineName,
		"panic_type":     "recovered",
	}

	// Include stack trace only in non-production mode
	if len(stack) > 0 && !isProduction {
		stackStr := string(stack)

		const maxStackLen = 4096
		if len(stackStr) > maxStackLen {
			stackStr = stackStr[:maxStackLen] + "\n...[truncated]"
		}

		tags["stack_trace"] = stackStr
	}

	reporter.CaptureException(ctx, err, tags)
}

// panicError wraps a panic value as an error for reporting.
type panicError struct {
	message string
}

// Error returns the panic error message.
func (e *panicError) Error() string {
	return e.message
}

func toPanicError(panicValue any, isProduction bool) error {
	if isProduction {
		return &panicError{message: redactedPanicMsg}
	}

	// Guard against typed-nil error values: an interface holding (type=*MyError, value=nil)
	// would pass the type assertion but panic on .Error(). Use reflect to detect this.
	if err, ok := panicValue.(error); ok && !isTypedNil(panicValue) {
		return err
	}

	if message, ok := panicValue.(string); ok {
		return &panicError{message: message}
	}

	return &panicError{message: "panic: " + formatPanicValue(panicValue)}
}

// isTypedNil returns true if v is an interface holding a nil pointer/nil value.
func isTypedNil(v any) bool {
	if v == nil {
		return false // untyped nil is not a typed nil
	}

	rv := reflect.ValueOf(v)

	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
		return rv.IsNil()
	default:
		return false
	}
}

// formatPanicValue formats a panic value as a string.
func formatPanicValue(value any) string {
	if value == nil {
		return "<nil>"
	}

	// Guard against typed-nil values that would panic on method calls.
	if isTypedNil(value) {
		return fmt.Sprintf("<%T>(nil)", value)
	}

	switch val := value.(type) {
	case string:
		return val
	case error:
		return val.Error()
	default:
		return fmt.Sprintf("%v", value)
	}
}
