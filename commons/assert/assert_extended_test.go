//go:build unit

package assert

import (
	"context"
	"strings"
	"testing"

	constant "github.com/LerianStudio/lib-commons/v4/commons/constants"
	"github.com/LerianStudio/lib-commons/v4/commons/runtime"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric/noop"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"

	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/opentelemetry/metrics"
)

func newTestMetricsFactory(t *testing.T) *metrics.MetricsFactory {
	t.Helper()

	meter := noop.NewMeterProvider().Meter("test")
	factory, err := metrics.NewMetricsFactory(meter, &libLog.NopLogger{})
	require.NoError(t, err, "newTestMetricsFactory failed")

	return factory
}

// --- AssertionError Tests ---

func TestAssertionError_NilReceiver(t *testing.T) {
	t.Parallel()

	var entry *AssertionError
	msg := entry.Error()
	require.Equal(t, ErrAssertionFailed.Error(), msg)
}

func TestAssertionError_WithoutDetails(t *testing.T) {
	t.Parallel()

	entry := &AssertionError{
		Assertion: "That",
		Message:   "some message",
		Component: "comp",
		Operation: "op",
		Details:   "",
	}

	msg := entry.Error()
	require.Equal(t, "assertion failed: some message", msg)
}

func TestAssertionError_WithDetails(t *testing.T) {
	t.Parallel()

	entry := &AssertionError{
		Assertion: "NotNil",
		Message:   "value required",
		Component: "comp",
		Operation: "op",
		Details:   "    key=value",
	}

	msg := entry.Error()
	require.Contains(t, msg, "assertion failed: value required")
	require.Contains(t, msg, "key=value")
}

func TestAssertionError_Unwrap(t *testing.T) {
	t.Parallel()

	entry := &AssertionError{Message: "test"}
	require.ErrorIs(t, entry, ErrAssertionFailed)
}

// --- Halt Tests ---

func TestHalt_NilError_NoEffect(t *testing.T) {
	t.Parallel()

	asserter := New(context.Background(), nil, "test", "halt")
	// Halt with nil error should be a no-op, no panic or goexit.
	asserter.Halt(nil)
}

// --- truncateValue Tests ---

func TestTruncateValue_ShortValue(t *testing.T) {
	t.Parallel()

	result := truncateValue("hello")
	require.Equal(t, "hello", result)
}

func TestTruncateValue_ExactMaxLength(t *testing.T) {
	t.Parallel()

	val := strings.Repeat("a", maxValueLength)
	result := truncateValue(val)
	require.Equal(t, val, result)
}

func TestTruncateValue_LongValue(t *testing.T) {
	t.Parallel()

	val := strings.Repeat("b", maxValueLength+50)
	result := truncateValue(val)
	require.Len(t, result, maxValueLength+len("... (truncated 50 chars)"))
	require.Contains(t, result, "... (truncated 50 chars)")
}

func TestTruncateValue_NonStringType(t *testing.T) {
	t.Parallel()

	result := truncateValue(42)
	require.Equal(t, "42", result)
}

// --- values Tests ---

func TestValues_NilAsserter(t *testing.T) {
	t.Parallel()

	var asserter *Asserter
	ctx, logger, component, operation := asserter.values(context.Background())
	require.NotNil(t, ctx)
	require.Nil(t, logger)
	require.Empty(t, component)
	require.Empty(t, operation)
}

func TestValues_NilAsserterNilCtx(t *testing.T) {
	t.Parallel()

	var asserter *Asserter
	//nolint:staticcheck // intentionally passing nil ctx
	ctx, _, _, _ := asserter.values(nil)
	require.NotNil(t, ctx)
}

func TestValues_WithAsserterNilCtx(t *testing.T) {
	t.Parallel()

	logger := &testLogger{}
	asserter := New(context.Background(), logger, "comp", "op")
	//nolint:staticcheck // intentionally passing nil ctx
	ctx, l, c, o := asserter.values(nil)
	require.NotNil(t, ctx)
	require.Equal(t, logger, l)
	require.Equal(t, "comp", c)
	require.Equal(t, "op", o)
}

func TestValues_BothNilFallsToBackground(t *testing.T) {
	t.Parallel()

	asserter := &Asserter{
		ctx:       nil,
		logger:    nil,
		component: "",
		operation: "",
	}
	//nolint:staticcheck // intentionally passing nil ctx
	ctx, _, _, _ := asserter.values(nil)
	require.NotNil(t, ctx)
}

// --- SanitizeMetricLabel Tests ---

func TestSanitizeMetricLabel_ShortLabel(t *testing.T) {
	t.Parallel()

	result := constant.SanitizeMetricLabel("short")
	require.Equal(t, "short", result)
}

func TestSanitizeMetricLabel_ExactMaxLength(t *testing.T) {
	t.Parallel()

	val := strings.Repeat("x", constant.MaxMetricLabelLength)
	result := constant.SanitizeMetricLabel(val)
	require.Equal(t, val, result)
}

func TestSanitizeMetricLabel_TruncatesLongLabel(t *testing.T) {
	t.Parallel()

	val := strings.Repeat("y", constant.MaxMetricLabelLength+20)
	result := constant.SanitizeMetricLabel(val)
	require.Len(t, result, constant.MaxMetricLabelLength)
	require.Equal(t, strings.Repeat("y", constant.MaxMetricLabelLength), result)
}

// --- assertionStatusMessage Tests ---

func TestAssertionStatusMessage_ComponentAndOperation(t *testing.T) {
	t.Parallel()

	msg := assertionStatusMessage("comp", "op")
	require.Equal(t, "assertion failed in comp/op", msg)
}

func TestAssertionStatusMessage_ComponentOnly(t *testing.T) {
	t.Parallel()

	msg := assertionStatusMessage("comp", "")
	require.Equal(t, "assertion failed in comp", msg)
}

func TestAssertionStatusMessage_OperationOnly(t *testing.T) {
	t.Parallel()

	msg := assertionStatusMessage("", "op")
	require.Equal(t, "assertion failed in op", msg)
}

func TestAssertionStatusMessage_Neither(t *testing.T) {
	t.Parallel()

	msg := assertionStatusMessage("", "")
	require.Equal(t, "assertion failed", msg)
}

// --- InitAssertionMetrics / ResetAssertionMetrics / GetAssertionMetrics Tests ---

func TestInitAssertionMetrics_NilFactory(t *testing.T) {
	// Not parallel - modifies global state.
	ResetAssertionMetrics()
	defer ResetAssertionMetrics()

	InitAssertionMetrics(nil)
	require.Nil(t, GetAssertionMetrics())
}

func TestInitAssertionMetrics_ValidFactory(t *testing.T) {
	// Not parallel - modifies global state.
	ResetAssertionMetrics()
	defer ResetAssertionMetrics()

	factory := newTestMetricsFactory(t)
	InitAssertionMetrics(factory)

	am := GetAssertionMetrics()
	require.NotNil(t, am)
	require.Equal(t, factory, am.factory)
}

func TestInitAssertionMetrics_DoubleInit_NoOverwrite(t *testing.T) {
	// Not parallel - modifies global state.
	ResetAssertionMetrics()
	defer ResetAssertionMetrics()

	factory1 := newTestMetricsFactory(t)
	factory2 := newTestMetricsFactory(t)

	InitAssertionMetrics(factory1)
	InitAssertionMetrics(factory2)

	am := GetAssertionMetrics()
	require.NotNil(t, am)
	require.Equal(t, factory1, am.factory, "second init should not overwrite")
}

func TestResetAssertionMetrics(t *testing.T) {
	// Not parallel - modifies global state.
	factory := newTestMetricsFactory(t)
	InitAssertionMetrics(factory)

	ResetAssertionMetrics()
	require.Nil(t, GetAssertionMetrics())
}

// --- RecordAssertionFailed Tests ---

func TestRecordAssertionFailed_NilMetrics(t *testing.T) {
	t.Parallel()

	// Should be a no-op, no panic.
	var am *AssertionMetrics
	am.RecordAssertionFailed(context.Background(), "comp", "op", "That")
}

func TestRecordAssertionFailed_NilFactory(t *testing.T) {
	t.Parallel()

	am := &AssertionMetrics{factory: nil}
	// Should be a no-op, no panic.
	am.RecordAssertionFailed(context.Background(), "comp", "op", "That")
}

func TestRecordAssertionFailed_WithFactory(t *testing.T) {
	// Not parallel - modifies global state.
	ResetAssertionMetrics()
	defer ResetAssertionMetrics()

	factory := newTestMetricsFactory(t)
	InitAssertionMetrics(factory)

	am := GetAssertionMetrics()
	require.NotNil(t, am)
	// Should not panic.
	am.RecordAssertionFailed(context.Background(), "comp", "op", "That")
}

// --- recordAssertionMetric Tests ---

func TestRecordAssertionMetric_NoMetricsInitialized(t *testing.T) {
	// Not parallel - modifies global state.
	ResetAssertionMetrics()
	defer ResetAssertionMetrics()

	// Should be a no-op, no panic.
	recordAssertionMetric(context.Background(), "comp", "op", "That")
}

func TestRecordAssertionMetric_WithMetrics(t *testing.T) {
	// Not parallel - modifies global state.
	ResetAssertionMetrics()
	defer ResetAssertionMetrics()

	factory := newTestMetricsFactory(t)
	InitAssertionMetrics(factory)

	// Should not panic.
	recordAssertionMetric(context.Background(), "comp", "op", "NotNil")
}

// --- recordAssertionToSpan Tests ---

func TestRecordAssertionToSpan_NoSpanInContext(t *testing.T) {
	t.Parallel()

	// Background context has a no-op span, which is not recording.
	// Should be a no-op, no panic.
	recordAssertionToSpan(context.Background(), "That", "test message", nil, "comp", "op")
}

func TestRecordAssertionToSpan_WithRecordingSpan(t *testing.T) {
	t.Parallel()

	tp := tracesdk.NewTracerProvider()
	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	// Should record event and error on the span, no panic.
	recordAssertionToSpan(ctx, "NotNil", "value is nil", nil, "comp", "op")
}

func TestRecordAssertionToSpan_WithStack(t *testing.T) {
	t.Parallel()

	tp := tracesdk.NewTracerProvider()
	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	stack := []byte("goroutine 1:\n  main.go:10")
	recordAssertionToSpan(ctx, "That", "condition false", stack, "comp", "op")
}

func TestRecordAssertionToSpan_EmptyComponentAndOperation(t *testing.T) {
	t.Parallel()

	tp := tracesdk.NewTracerProvider()
	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	recordAssertionToSpan(ctx, "Never", "unreachable", nil, "", "")
}

// --- logAssertion Tests ---

func TestLogAssertion_WithNilLogger(t *testing.T) {
	t.Parallel()

	// Writes to stderr, should not panic.
	logAssertion(nil, "test message for stderr")
}

func TestLogAssertion_WithLogger(t *testing.T) {
	t.Parallel()

	logger := &testLogger{}
	logAssertion(logger, "test message for logger")
	require.Len(t, logger.messages, 1)
	require.Equal(t, "test message for logger", logger.messages[0])
}

// --- New Tests ---

func TestNew_NilContext(t *testing.T) {
	t.Parallel()

	//nolint:staticcheck // intentionally passing nil ctx
	asserter := New(nil, nil, "comp", "op")
	require.NotNil(t, asserter)
	require.NotNil(t, asserter.ctx)
}

func TestNew_WithAllFields(t *testing.T) {
	t.Parallel()

	logger := &testLogger{}
	ctx := context.Background()
	asserter := New(ctx, logger, "comp", "op")
	require.Equal(t, ctx, asserter.ctx)
	require.Equal(t, logger, asserter.logger)
	require.Equal(t, "comp", asserter.component)
	require.Equal(t, "op", asserter.operation)
}

// --- formatKeyValueLines Tests ---

func TestFormatKeyValueLines_Empty(t *testing.T) {
	t.Parallel()

	result := formatKeyValueLines(nil)
	require.Empty(t, result)
}

func TestFormatKeyValueLines_SinglePair(t *testing.T) {
	t.Parallel()

	result := formatKeyValueLines([]any{"key", "value"})
	require.Equal(t, "    key=value", result)
}

func TestFormatKeyValueLines_MultiplePairs(t *testing.T) {
	t.Parallel()

	result := formatKeyValueLines([]any{"k1", "v1", "k2", "v2"})
	require.Contains(t, result, "k1=v1")
	require.Contains(t, result, "k2=v2")
}

func TestFormatKeyValueLines_OddCount(t *testing.T) {
	t.Parallel()

	result := formatKeyValueLines([]any{"k1", "v1", "orphan"})
	require.Contains(t, result, "k1=v1")
	require.Contains(t, result, "orphan=MISSING_VALUE")
}

// --- recordAssertionObservability Tests ---

func TestRecordAssertionObservability_NoMetricsNoSpan(t *testing.T) {
	// Not parallel - modifies global state.
	ResetAssertionMetrics()
	defer ResetAssertionMetrics()

	// Should not panic.
	recordAssertionObservability(context.Background(), "That", "test", nil, "comp", "op")
}

// --- isNil Tests ---

func TestIsNil_UntypedNil(t *testing.T) {
	t.Parallel()
	require.True(t, isNil(nil))
}

func TestIsNil_NonNilInt(t *testing.T) {
	t.Parallel()
	require.False(t, isNil(42))
}

func TestIsNil_NonNilString(t *testing.T) {
	t.Parallel()
	require.False(t, isNil("hello"))
}

func TestIsNil_NonNilStruct(t *testing.T) {
	t.Parallel()

	type s struct{}
	require.False(t, isNil(s{}))
}

// --- shouldIncludeStack Tests ---

func TestShouldIncludeStack_NonProduction(t *testing.T) {
	t.Setenv("ENV", "development")
	t.Setenv("GO_ENV", "")

	require.True(t, shouldIncludeStack())
}

func TestShouldIncludeStack_ProductionENV(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("GO_ENV", "")

	require.False(t, shouldIncludeStack())
}

func TestShouldIncludeStack_ProductionGOENV(t *testing.T) {
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "production")

	require.False(t, shouldIncludeStack())
}

func TestShouldIncludeStack_ProductionCaseInsensitive(t *testing.T) {
	t.Setenv("ENV", "Production")
	t.Setenv("GO_ENV", "")

	require.False(t, shouldIncludeStack())
}

func TestShouldIncludeStack_RuntimeProductionMode(t *testing.T) {
	// Not parallel - modifies global state.
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")

	runtime.SetProductionMode(true)
	defer runtime.SetProductionMode(false)

	require.False(t, shouldIncludeStack(), "should suppress stacks when runtime.IsProductionMode() is true")
}

func TestShouldIncludeStack_RuntimeProductionModeOverridesEnv(t *testing.T) {
	// Not parallel - modifies global state.
	// Even though env vars say non-production, runtime mode takes priority.
	t.Setenv("ENV", "development")
	t.Setenv("GO_ENV", "development")

	runtime.SetProductionMode(true)
	defer runtime.SetProductionMode(false)

	require.False(t, shouldIncludeStack(), "runtime production mode should override env vars")
}

func TestShouldIncludeStack_EnvFallbackWhenRuntimeNotSet(t *testing.T) {
	// Not parallel - modifies global state.
	runtime.SetProductionMode(false)
	defer runtime.SetProductionMode(false)

	t.Setenv("ENV", "production")
	t.Setenv("GO_ENV", "")

	require.False(t, shouldIncludeStack(), "env var fallback should still detect production")
}

func TestShouldIncludeStack_NonProductionWhenBothDisabled(t *testing.T) {
	// Not parallel - modifies global state.
	runtime.SetProductionMode(false)
	defer runtime.SetProductionMode(false)

	t.Setenv("ENV", "development")
	t.Setenv("GO_ENV", "")

	require.True(t, shouldIncludeStack(), "should include stacks in non-production mode")
}

// --- withContextPairs Tests ---

func TestWithContextPairs_AllFields(t *testing.T) {
	t.Parallel()

	result := withContextPairs("That", "comp", "op", []any{"k1", "v1"})
	// Should contain: assertion, That, component, comp, operation, op, k1, v1
	require.Len(t, result, 8)
}

func TestWithContextPairs_EmptyComponent(t *testing.T) {
	t.Parallel()

	result := withContextPairs("NotNil", "", "op", nil)
	// Should contain: assertion, NotNil, operation, op
	require.Len(t, result, 4)
}

func TestWithContextPairs_EmptyOperation(t *testing.T) {
	t.Parallel()

	result := withContextPairs("NotNil", "comp", "", nil)
	// Should contain: assertion, NotNil, component, comp
	require.Len(t, result, 4)
}

func TestWithContextPairs_BothEmpty(t *testing.T) {
	t.Parallel()

	result := withContextPairs("Never", "", "", nil)
	// Should contain: assertion, Never
	require.Len(t, result, 2)
}
