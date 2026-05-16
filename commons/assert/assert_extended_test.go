//go:build unit

package assert

import (
	"context"
	"strings"
	"testing"

	constant "github.com/LerianStudio/lib-commons/v5/commons/constants"
	libLog "github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric/noop"
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
	// Verify the metrics singleton is functional.
	am.RecordAssertionFailed(context.Background(), "comp", "op", "That")
}

func TestInitAssertionMetrics_DoubleInit_NoOverwrite(t *testing.T) {
	// Not parallel - modifies global state.
	ResetAssertionMetrics()
	defer ResetAssertionMetrics()

	factory1 := newTestMetricsFactory(t)
	factory2 := newTestMetricsFactory(t)

	InitAssertionMetrics(factory1)
	first := GetAssertionMetrics()
	require.NotNil(t, first)

	InitAssertionMetrics(factory2)

	am := GetAssertionMetrics()
	require.Same(t, first, am, "second init should not overwrite first init")
	// Must still be functional.
	am.RecordAssertionFailed(context.Background(), "comp", "op", "That")
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

	am := &AssertionMetrics{} // zero-value: factory is nil
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
