package assert

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	goruntime "runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	constant "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"

	"github.com/LerianStudio/lib-commons/v5/commons/runtime"
)

// Logger defines the minimal logging interface required by assertions.
// This interface is satisfied by commons/log.Logger.
type Logger interface {
	Log(ctx context.Context, level log.Level, msg string, fields ...log.Field)
}

// Asserter evaluates invariants and emits telemetry on failure.
type Asserter struct {
	ctx       context.Context
	logger    Logger
	component string
	operation string
}

// ErrAssertionFailed is the sentinel error for failed assertions.
var ErrAssertionFailed = errors.New("assertion failed")

// AssertionError represents a failed assertion with rich context.
type AssertionError struct {
	Assertion string
	Message   string
	Component string
	Operation string
	Details   string
}

// Error returns the formatted assertion failure message.
func (entry *AssertionError) Error() string {
	if entry == nil {
		return ErrAssertionFailed.Error()
	}

	if entry.Details == "" {
		return "assertion failed: " + entry.Message
	}

	return "assertion failed: " + entry.Message + "\n" + entry.Details
}

// Unwrap returns the sentinel assertion error for errors.Is.
func (entry *AssertionError) Unwrap() error {
	return ErrAssertionFailed
}

// New creates an Asserter with context, logging, and labels.
// component and operation are used for telemetry labeling.
//
//nolint:contextcheck // Intentionally creates a fallback context when nil is passed
func New(ctx context.Context, logger Logger, component, operation string) *Asserter {
	if ctx == nil {
		ctx = context.Background()
	}

	return &Asserter{
		ctx:       ctx,
		logger:    logger,
		component: component,
		operation: operation,
	}
}

// That returns an error if ok is false. Use for general-purpose assertions.
//
// Example:
//
//	if err := asserter.That(ctx, len(items) > 0, "items must not be empty", "count", len(items)); err != nil {
//		return err
//	}
func (asserter *Asserter) That(ctx context.Context, ok bool, msg string, kv ...any) error {
	if ok {
		return nil
	}

	return asserter.fail(ctx, "That", msg, kv...)
}

// NotNil returns an error if v is nil. This function correctly handles both untyped nil
// and typed nil (nil interface values with concrete types).
//
// Example:
//
//	if err := asserter.NotNil(ctx, config, "config must be initialized"); err != nil {
//		return err
//	}
func (asserter *Asserter) NotNil(ctx context.Context, v any, msg string, kv ...any) error {
	if !isNil(v) {
		return nil
	}

	return asserter.fail(ctx, "NotNil", msg, kv...)
}

// NotEmpty returns an error if s is an empty string.
//
// Example:
//
//	if err := asserter.NotEmpty(ctx, userID, "userID must be provided"); err != nil {
//		return err
//	}
func (asserter *Asserter) NotEmpty(ctx context.Context, s, msg string, kv ...any) error {
	if s != "" {
		return nil
	}

	return asserter.fail(ctx, "NotEmpty", msg, kv...)
}

// NoError returns an error if err is not nil. The error message and type are
// automatically included in the assertion context for debugging.
//
// Example:
//
//	if err := asserter.NoError(ctx, err, "compute must succeed", "input", input); err != nil {
//		return err
//	}
func (asserter *Asserter) NoError(ctx context.Context, err error, msg string, kv ...any) error {
	if err == nil {
		return nil
	}

	// Prepend error and error_type to key-value pairs for richer debugging
	// errorKVPairs: 2 pairs added (error + error_type), each pair = 2 elements
	const errorKVPairs = 4

	kvWithError := make([]any, 0, len(kv)+errorKVPairs)
	kvWithError = append(kvWithError, "error", err.Error())
	kvWithError = append(kvWithError, "error_type", fmt.Sprintf("%T", err))
	kvWithError = append(kvWithError, kv...)

	return asserter.fail(ctx, "NoError", msg, kvWithError...)
}

// Never always returns an error. Use for code paths that should be unreachable.
//
// Example:
//
//	return asserter.Never(ctx, "unhandled status", "status", status)
func (asserter *Asserter) Never(ctx context.Context, msg string, kv ...any) error {
	return asserter.fail(ctx, "Never", msg, kv...)
}

// Halt terminates the current goroutine if err is not nil.
// Use this after a failed assertion in goroutines to prevent further execution.
func (asserter *Asserter) Halt(err error) {
	if err != nil {
		goruntime.Goexit()
	}
}

const maxValueLength = 200 // Truncate values longer than this

// truncateValue truncates long values for logging safety.
// This prevents log bloat and reduces risk of sensitive data exposure.
func truncateValue(v any) string {
	s := fmt.Sprintf("%v", v)
	if len(s) <= maxValueLength {
		return s
	}

	return s[:maxValueLength] + "... (truncated " + strconv.Itoa(len(s)-maxValueLength) + " chars)"
}

func (asserter *Asserter) fail(ctx context.Context, assertion, msg string, kv ...any) error {
	ctx, logger, component, operation := asserter.values(ctx)
	contextPairs := withContextPairs(assertion, component, operation, kv)
	details := formatKeyValueLines(contextPairs)

	stack := []byte(nil)
	if shouldIncludeStack() {
		stack = debug.Stack()
	}

	// Emit structured fields for log aggregation; fall back to single-string format
	// when no logger is available (stderr path) or for the stack trace supplement.
	logAssertionStructured(logger, assertion, component, operation, msg, details)

	if len(stack) > 0 && logger != nil {
		logger.Log(context.Background(), log.LevelError, "assertion stack trace",
			log.String("assertion_type", assertion),
			log.String("stack_trace", string(stack)),
		)
	}

	recordAssertionObservability(ctx, assertion, msg, stack, component, operation)

	return &AssertionError{
		Assertion: assertion,
		Message:   msg,
		Component: component,
		Operation: operation,
		Details:   details,
	}
}

func (asserter *Asserter) values(ctx context.Context) (context.Context, Logger, string, string) {
	if asserter == nil {
		if ctx == nil {
			ctx = context.Background()
		}

		return ctx, nil, "", ""
	}

	if ctx == nil {
		ctx = asserter.ctx
	}

	if ctx == nil {
		ctx = context.Background()
	}

	return ctx, asserter.logger, asserter.component, asserter.operation
}

// shouldIncludeStack controls whether assertion failures include a stack trace.
//
// Stack traces are opt-out: they are included by default and suppressed when
// production mode is detected. This is intentional because during development
// and testing, stack traces are invaluable for debugging assertion failures,
// while in production they add noise and may expose internal paths.
//
// To disable stack traces in production, use either:
//   - runtime.SetProductionMode(true) during application startup (preferred)
//   - Set ENV=production or GO_ENV=production environment variables
func shouldIncludeStack() bool {
	// Primary check: use runtime.IsProductionMode() which is explicitly
	// set during application startup via runtime.SetProductionMode(true).
	if runtime.IsProductionMode() {
		return false
	}

	// Fallback: check environment variables for cases where production mode
	// has not been explicitly configured via the runtime package.
	env := strings.TrimSpace(os.Getenv("ENV"))
	goEnv := strings.TrimSpace(os.Getenv("GO_ENV"))

	return !strings.EqualFold(env, "production") && !strings.EqualFold(goEnv, "production")
}

// contextPairsCapacity is the capacity for the fixed context pairs (assertion, component, operation).
const contextPairsCapacity = 6

func withContextPairs(assertion, component, operation string, kv []any) []any {
	contextPairs := make([]any, 0, len(kv)+contextPairsCapacity)
	contextPairs = append(contextPairs, "assertion", assertion)

	if component != "" {
		contextPairs = append(contextPairs, "component", component)
	}

	if operation != "" {
		contextPairs = append(contextPairs, "operation", operation)
	}

	contextPairs = append(contextPairs, kv...)

	return contextPairs
}

func formatKeyValueLines(kv []any) string {
	if len(kv) == 0 {
		return ""
	}

	var sb strings.Builder

	for i := 0; i < len(kv); i += 2 {
		if i > 0 {
			sb.WriteString("\n")
		}

		var value any
		if i+1 < len(kv) {
			value = kv[i+1]
		} else {
			value = "MISSING_VALUE"
		}

		fmt.Fprintf(&sb, "    %v=%v", kv[i], truncateValue(value))
	}

	return sb.String()
}

// logAssertionStructured emits assertion failure as individual structured log fields
// for better searchability in log aggregation systems (Loki, Elasticsearch, etc.).
func logAssertionStructured(logger Logger, assertion, component, operation, msg, details string) {
	if logger == nil {
		// Fall back to stderr for emergency visibility
		fmt.Fprintln(os.Stderr, "ASSERTION FAILED: "+msg)

		return
	}

	fields := []log.Field{
		log.String("assertion_type", assertion),
		log.String("message", msg),
	}

	if component != "" {
		fields = append(fields, log.String("component", component))
	}

	if operation != "" {
		fields = append(fields, log.String("operation", operation))
	}

	if details != "" {
		fields = append(fields, log.String("details", details))
	}

	logger.Log(context.Background(), log.LevelError, "ASSERTION FAILED", fields...)
}

// logAssertion is kept for backward compatibility with code paths that only
// have a pre-formatted message string (e.g. when logger is nil and we write to stderr).
func logAssertion(logger Logger, message string) {
	if logger != nil {
		logger.Log(context.Background(), log.LevelError, message)
		return
	}

	fmt.Fprintln(os.Stderr, message)
}

// isNil checks if a value is nil, handling both untyped nil and typed nil
// (nil interface values with concrete types).
func isNil(v any) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
		return rv.IsNil()
	default:
		return false
	}
}

// AssertionSpanEventName is the event name used when recording assertion failures on spans.
const AssertionSpanEventName = constant.EventAssertionFailed

// AssertionMetrics provides assertion-related metrics using OpenTelemetry.
// It wraps lib-commons' MetricsFactory for consistent metric handling.
type AssertionMetrics struct {
	factory *metrics.MetricsFactory
}

// assertionFailedMetric defines the metric for counting failed assertions.
var assertionFailedMetric = metrics.Metric{
	Name:        constant.MetricAssertionFailedTotal,
	Unit:        "1",
	Description: "Total number of failed assertions",
}

var (
	assertionMetricsInstance *AssertionMetrics
	assertionMetricsMu       sync.RWMutex
)

// InitAssertionMetrics initializes assertion metrics with the provided MetricsFactory.
// This should be called once during application startup after telemetry is initialized.
func InitAssertionMetrics(factory *metrics.MetricsFactory) {
	assertionMetricsMu.Lock()
	defer assertionMetricsMu.Unlock()

	if factory == nil {
		return
	}

	if assertionMetricsInstance != nil {
		return
	}

	assertionMetricsInstance = &AssertionMetrics{factory: factory}
}

// GetAssertionMetrics returns the singleton AssertionMetrics instance.
// Returns nil if InitAssertionMetrics has not been called.
func GetAssertionMetrics() *AssertionMetrics {
	assertionMetricsMu.RLock()
	defer assertionMetricsMu.RUnlock()

	return assertionMetricsInstance
}

// ResetAssertionMetrics clears the assertion metrics singleton (useful for tests).
func ResetAssertionMetrics() {
	assertionMetricsMu.Lock()
	defer assertionMetricsMu.Unlock()

	assertionMetricsInstance = nil
}

// RecordAssertionFailed increments the assertion_failed_total counter with labels.
// If metrics are not initialized, this is a no-op.
func (am *AssertionMetrics) RecordAssertionFailed(
	ctx context.Context,
	component, operation, assertion string,
) {
	if am == nil || am.factory == nil {
		return
	}

	counter, err := am.factory.Counter(assertionFailedMetric)
	if err != nil {
		logAssertion(nil, fmt.Sprintf("failed to create assertion metric counter: %v", err))
		return
	}

	err = counter.
		WithLabels(map[string]string{
			"component": constant.SanitizeMetricLabel(component),
			"operation": constant.SanitizeMetricLabel(operation),
			"assertion": constant.SanitizeMetricLabel(assertion),
		}).
		AddOne(ctx)
	if err != nil {
		logAssertion(nil, fmt.Sprintf("failed to record assertion metric: %v", err))
		return
	}
}

func recordAssertionMetric(ctx context.Context, component, operation, assertion string) {
	am := GetAssertionMetrics()
	if am != nil {
		am.RecordAssertionFailed(ctx, component, operation, assertion)
	}
}

func recordAssertionObservability(
	ctx context.Context,
	assertion, message string,
	stack []byte,
	component, operation string,
) {
	recordAssertionMetric(ctx, component, operation, assertion)
	recordAssertionToSpan(ctx, assertion, message, stack, component, operation)
}

func recordAssertionToSpan(
	ctx context.Context,
	assertion, message string,
	stack []byte,
	component, operation string,
) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}

	attrs := []attribute.KeyValue{
		attribute.String("assertion.name", assertion),
		attribute.String("assertion.message", message),
	}

	if component != "" {
		attrs = append(attrs, attribute.String("assertion.component", component))
	}

	if operation != "" {
		attrs = append(attrs, attribute.String("assertion.operation", operation))
	}

	if len(stack) > 0 {
		attrs = append(attrs, attribute.String("assertion.stack", string(stack)))
	}

	span.AddEvent(AssertionSpanEventName, trace.WithAttributes(attrs...))
	span.RecordError(fmt.Errorf("%w: %s", ErrAssertionFailed, message))
	span.SetStatus(codes.Error, assertionStatusMessage(component, operation))
}

func assertionStatusMessage(component, operation string) string {
	switch {
	case component != "" && operation != "":
		return fmt.Sprintf("assertion failed in %s/%s", component, operation)
	case component != "":
		return "assertion failed in " + component
	case operation != "":
		return "assertion failed in " + operation
	default:
		return "assertion failed"
	}
}
