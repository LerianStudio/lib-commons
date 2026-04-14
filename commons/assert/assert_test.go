//go:build unit

package assert

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// errTest is a test error for assertions.
var errTest = errors.New("test error")

// errSpecificTest is a specific test error for assertions.
var errSpecificTest = errors.New("specific test error")

type testLogger struct {
	messages []string
}

func (l *testLogger) Log(_ context.Context, _ log.Level, msg string, _ ...log.Field) {
	l.messages = append(l.messages, msg)
}

func newTestAsserter(logger Logger) *Asserter {
	return New(context.Background(), logger, "test-component", "test-operation")
}

func newTestAsserterWithLogger() (*Asserter, *testLogger) {
	logger := &testLogger{}
	return newTestAsserter(logger), logger
}

// TestThat_Pass verifies That returns nil when condition is true.
func TestThat_Pass(t *testing.T) {
	t.Parallel()

	a := newTestAsserter(nil)
	require.NoError(t, a.That(context.Background(), true, "should not fail"))
}

// TestThat_Fail verifies That returns an error when condition is false.
func TestThat_Fail(t *testing.T) {
	t.Parallel()

	a, _ := newTestAsserterWithLogger()
	err := a.That(context.Background(), false, "should fail")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrAssertionFailed)
}

// TestThat_ErrorMessage verifies the error message contains the expected content.
func TestThat_ErrorMessage(t *testing.T) {
	t.Parallel()

	a, _ := newTestAsserterWithLogger()
	err := a.That(context.Background(), false, "test message", "key1", "value1", "key2", 42)
	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, "assertion failed:")
	require.Contains(t, msg, "test message")
	require.Contains(t, msg, "assertion=That")
	require.Contains(t, msg, "key1=value1")
	require.Contains(t, msg, "key2=42")
}

// TestThat_LogIncludesStackTrace verifies stack trace is logged in non-production.
// With structured logging, the stack trace is emitted as a separate log call
// with message "assertion stack trace" and a stack_trace field.
func TestThat_LogIncludesStackTrace(t *testing.T) {
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")

	a, logger := newTestAsserterWithLogger()
	err := a.That(context.Background(), false, "test message", "key1", "value1")
	require.Error(t, err)
	require.NotEmpty(t, logger.messages)
	// First log is the structured assertion failure
	require.Contains(t, logger.messages[0], "ASSERTION FAILED")
	// Second log is the stack trace (emitted separately for structured logging)
	require.Len(t, logger.messages, 2, "should have assertion failure + stack trace logs")
	require.Contains(t, logger.messages[1], "assertion stack trace")
}

// TestNotNil_Pass verifies NotNil returns nil for non-nil values.
func TestNotNil_Pass(t *testing.T) {
	t.Parallel()

	asserter := newTestAsserter(nil)
	require.NoError(t, asserter.NotNil(context.Background(), "hello", "string should not be nil"))
	require.NoError(t, asserter.NotNil(context.Background(), 42, "int should not be nil"))

	x := new(int)
	require.NoError(t, asserter.NotNil(context.Background(), x, "pointer should not be nil"))

	s := []int{1, 2, 3}
	require.NoError(t, asserter.NotNil(context.Background(), s, "slice should not be nil"))

	m := map[string]int{"a": 1}
	require.NoError(t, asserter.NotNil(context.Background(), m, "map should not be nil"))
}

// TestNotNil_Fail verifies NotNil returns an error for nil values.
func TestNotNil_Fail(t *testing.T) {
	t.Parallel()

	a, _ := newTestAsserterWithLogger()
	err := a.NotNil(context.Background(), nil, "should fail for nil")
	require.Error(t, err)
}

// TestNotNil_TypedNil verifies NotNil correctly handles typed nil.
// A typed nil is when an interface holds a nil pointer of a concrete type.
func TestNotNil_TypedNil(t *testing.T) {
	t.Parallel()

	asserter, _ := newTestAsserterWithLogger()

	var ptr *int

	var iface any = ptr // typed nil: interface is not nil, but value is

	err := asserter.NotNil(context.Background(), iface, "should fail for typed nil")
	require.Error(t, err)
}

// TestNotNil_TypedNilSlice verifies NotNil handles typed nil slices.
func TestNotNil_TypedNilSlice(t *testing.T) {
	t.Parallel()

	asserter, _ := newTestAsserterWithLogger()

	var s []int

	var iface any = s

	err := asserter.NotNil(context.Background(), iface, "should fail for typed nil slice")
	require.Error(t, err)
}

// TestNotNil_TypedNilMap verifies NotNil handles typed nil maps.
func TestNotNil_TypedNilMap(t *testing.T) {
	t.Parallel()

	asserter, _ := newTestAsserterWithLogger()

	var m map[string]int

	var iface any = m

	err := asserter.NotNil(context.Background(), iface, "should fail for typed nil map")
	require.Error(t, err)
}

// TestNotNil_TypedNilChan verifies NotNil handles typed nil channels.
func TestNotNil_TypedNilChan(t *testing.T) {
	t.Parallel()

	asserter, _ := newTestAsserterWithLogger()

	var ch chan int

	var iface any = ch

	err := asserter.NotNil(context.Background(), iface, "should fail for typed nil channel")
	require.Error(t, err)
}

// TestNotNil_TypedNilFunc verifies NotNil handles typed nil functions.
func TestNotNil_TypedNilFunc(t *testing.T) {
	t.Parallel()

	asserter, _ := newTestAsserterWithLogger()

	var fn func()

	var iface any = fn

	err := asserter.NotNil(context.Background(), iface, "should fail for typed nil function")
	require.Error(t, err)
}

// TestNotEmpty_Pass verifies NotEmpty returns nil for non-empty strings.
func TestNotEmpty_Pass(t *testing.T) {
	t.Parallel()

	a := newTestAsserter(nil)
	require.NoError(t, a.NotEmpty(context.Background(), "hello", "should not fail"))
	require.NoError(t, a.NotEmpty(context.Background(), " ", "whitespace is not empty"))
}

// TestNotEmpty_Fail verifies NotEmpty returns an error for empty strings.
func TestNotEmpty_Fail(t *testing.T) {
	t.Parallel()

	a, _ := newTestAsserterWithLogger()
	err := a.NotEmpty(context.Background(), "", "should fail for empty string")
	require.Error(t, err)
}

// TestNoError_Pass verifies NoError returns nil when error is nil.
func TestNoError_Pass(t *testing.T) {
	t.Parallel()

	a := newTestAsserter(nil)
	require.NoError(t, a.NoError(context.Background(), nil, "should not fail"))
}

// TestNoError_Fail verifies NoError returns an error when error is not nil.
func TestNoError_Fail(t *testing.T) {
	t.Parallel()

	a, _ := newTestAsserterWithLogger()
	err := a.NoError(context.Background(), errTest, "should fail")
	require.Error(t, err)
}

// TestNoError_MessageContainsError verifies the error message and type are included.
func TestNoError_MessageContainsError(t *testing.T) {
	t.Parallel()

	a, _ := newTestAsserterWithLogger()
	err := a.NoError(
		context.Background(),
		errSpecificTest,
		"operation failed",
		"context_key",
		"context_value",
	)
	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, "assertion failed:")
	require.Contains(t, msg, "operation failed")
	require.Contains(t, msg, "error=specific test error")
	require.Contains(t, msg, "error_type=*errors.errorString")
	require.Contains(t, msg, "context_key=context_value")
}

// TestNever_AlwaysFails verifies Never always returns an error.
func TestNever_AlwaysFails(t *testing.T) {
	t.Parallel()

	a, _ := newTestAsserterWithLogger()
	err := a.Never(context.Background(), "unreachable code reached")
	require.Error(t, err)
}

// TestNever_ErrorMessage verifies Never includes message and context.
func TestNever_ErrorMessage(t *testing.T) {
	t.Parallel()

	a, _ := newTestAsserterWithLogger()
	err := a.Never(context.Background(), "unreachable", "state", "invalid")
	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, "assertion failed:")
	require.Contains(t, msg, "unreachable")
	require.Contains(t, msg, "state=invalid")
}

// TestOddKeyValuePairs verifies handling of odd number of key-value pairs.
func TestOddKeyValuePairs(t *testing.T) {
	t.Parallel()

	a, _ := newTestAsserterWithLogger()
	err := a.That(context.Background(), false, "test", "key1", "value1", "key2")
	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, "key1=value1")
	require.Contains(t, msg, "key2=MISSING_VALUE")
}

// TestPositive tests the Positive predicate.
func TestPositive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		n        int64
		expected bool
	}{
		{"positive", 1, true},
		{"large positive", 1000000, true},
		{"max int64", math.MaxInt64, true},
		{"zero", 0, false},
		{"negative", -1, false},
		{"large negative", -1000000, false},
		{"min int64", math.MinInt64, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, Positive(tt.n))
		})
	}
}

// TestNonNegative tests the NonNegative predicate.
func TestNonNegative(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		n        int64
		expected bool
	}{
		{"positive", 1, true},
		{"max int64", math.MaxInt64, true},
		{"zero", 0, true},
		{"negative", -1, false},
		{"large negative", -1000000, false},
		{"min int64", math.MinInt64, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, NonNegative(tt.n))
		})
	}
}

// TestNotZero tests the NotZero predicate.
func TestNotZero(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		n        int64
		expected bool
	}{
		{"positive", 1, true},
		{"negative", -1, true},
		{"zero", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, NotZero(tt.n))
		})
	}
}

// TestInRange tests the InRange predicate.
func TestInRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		n        int64
		min      int64
		max      int64
		expected bool
	}{
		{"in range", 5, 1, 10, true},
		{"at min", 1, 1, 10, true},
		{"at max", 10, 1, 10, true},
		{"below range", 0, 1, 10, false},
		{"above range", 11, 1, 10, false},
		{"inverted range", 5, 10, 1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, InRange(tt.n, tt.min, tt.max))
		})
	}
}

// TestValidUUID tests ValidUUID predicate.
func TestValidUUID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		uuid     string
		expected bool
	}{
		{"valid UUID", "123e4567-e89b-12d3-a456-426614174000", true},
		{"valid UUID without hyphens", "123e4567e89b12d3a456426614174000", true},
		{"empty string", "", false},
		{"invalid format", "not-a-uuid", false},
		{"too short", "123e4567-e89b-12d3-a456-42661417400", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, ValidUUID(tt.uuid))
		})
	}
}

// TestValidAmount tests ValidAmount predicate.
func TestValidAmount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		amount   decimal.Decimal
		expected bool
	}{
		{"zero", decimal.Zero, true},
		{"max positive exponent", decimal.New(1, 18), true},
		{"min negative exponent", decimal.New(1, -18), true},
		{"too large exponent", decimal.New(1, 19), false},
		{"too small exponent", decimal.New(1, -19), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, ValidAmount(tt.amount))
		})
	}
}

// TestValidScale tests ValidScale predicate.
func TestValidScale(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		scale    int
		expected bool
	}{
		{"min scale", 0, true},
		{"max scale", 18, true},
		{"negative scale", -1, false},
		{"too large scale", 19, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, ValidScale(tt.scale))
		})
	}
}

// TestPositiveDecimal tests PositiveDecimal predicate.
func TestPositiveDecimal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		amount   decimal.Decimal
		expected bool
	}{
		{"positive", decimal.NewFromFloat(1.23), true},
		{"zero", decimal.Zero, false},
		{"negative", decimal.NewFromFloat(-1.23), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, PositiveDecimal(tt.amount))
		})
	}
}

// TestNonNegativeDecimal tests NonNegativeDecimal predicate.
func TestNonNegativeDecimal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		amount   decimal.Decimal
		expected bool
	}{
		{"positive", decimal.NewFromFloat(1.23), true},
		{"zero", decimal.Zero, true},
		{"negative", decimal.NewFromFloat(-1.23), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, NonNegativeDecimal(tt.amount))
		})
	}
}

// TestValidPort tests ValidPort predicate.
func TestValidPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		port     string
		expected bool
	}{
		{"valid port", "5432", true},
		{"min port", "1", true},
		{"max port", "65535", true},
		{"zero port", "0", false},
		{"negative", "-1", false},
		{"too large", "65536", false},
		{"non-numeric", "abc", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, ValidPort(tt.port))
		})
	}
}

// TestValidSSLMode tests ValidSSLMode predicate.
func TestValidSSLMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mode     string
		expected bool
	}{
		{"empty", "", true},
		{"disable", "disable", true},
		{"allow", "allow", true},
		{"prefer", "prefer", true},
		{"require", "require", true},
		{"verify-ca", "verify-ca", true},
		{"verify-full", "verify-full", true},
		{"invalid", "invalid", false},
		{"uppercase", "DISABLE", false},
		{"with spaces", " disable ", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, ValidSSLMode(tt.mode))
		})
	}
}

// TestPositiveInt tests PositiveInt predicate.
func TestPositiveInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		n        int
		expected bool
	}{
		{"positive", 1, true},
		{"zero", 0, false},
		{"negative", -1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, PositiveInt(tt.n))
		})
	}
}

// TestInRangeInt tests InRangeInt predicate.
func TestInRangeInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		n        int
		min      int
		max      int
		expected bool
	}{
		{"in range", 5, 1, 10, true},
		{"at min", 1, 1, 10, true},
		{"at max", 10, 1, 10, true},
		{"below range", 0, 1, 10, false},
		{"above range", 11, 1, 10, false},
		{"inverted range", 5, 10, 1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, InRangeInt(tt.n, tt.min, tt.max))
		})
	}
}
