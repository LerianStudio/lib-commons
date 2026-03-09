//go:build unit

package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	errBasePanic        = errors.New("base error")
	errDetailedMessage  = errors.New("detailed error message")
	errPanicError       = errors.New("error panic")
	errSensitiveDetails = errors.New("database password: secret123")
	errTestError        = errors.New("test error")
)

// testErrorReporter is a test implementation of ErrorReporter for these tests.
type testErrorReporter struct {
	mu           sync.RWMutex
	capturedErr  error
	capturedCtx  context.Context
	capturedTags map[string]string
	callCount    int
}

func (reporter *testErrorReporter) CaptureException(
	ctx context.Context,
	err error,
	tags map[string]string,
) {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()

	reporter.capturedErr = err
	reporter.capturedCtx = ctx
	reporter.capturedTags = tags
	reporter.callCount++
}

func (reporter *testErrorReporter) getCapturedErr() error {
	reporter.mu.RLock()
	defer reporter.mu.RUnlock()

	return reporter.capturedErr
}

func (reporter *testErrorReporter) getCapturedTags() map[string]string {
	reporter.mu.RLock()
	defer reporter.mu.RUnlock()

	// Return a defensive copy to prevent races with callers
	if reporter.capturedTags == nil {
		return nil
	}

	copyTags := make(map[string]string, len(reporter.capturedTags))
	for k, v := range reporter.capturedTags {
		copyTags[k] = v
	}

	return copyTags
}

func (reporter *testErrorReporter) getCallCount() int {
	reporter.mu.RLock()
	defer reporter.mu.RUnlock()

	return reporter.callCount
}

// TestSetAndGetErrorReporter tests basic SetErrorReporter and GetErrorReporter functionality.
//
//nolint:paralleltest // Cannot use t.Parallel() - modifies global errorReporterInstance
func TestSetAndGetErrorReporter(t *testing.T) {
	SetErrorReporter(nil)
	t.Cleanup(func() { SetErrorReporter(nil) })

	reporter := &testErrorReporter{}
	SetErrorReporter(reporter)

	got := GetErrorReporter()
	require.NotNil(t, got)
	assert.Equal(t, reporter, got)
}

// TestReportPanicToErrorService_NilContext tests reporting with nil context.
//
//nolint:paralleltest // Cannot use t.Parallel() - modifies global errorReporterInstance
func TestReportPanicToErrorService_NilContext(t *testing.T) {
	SetErrorReporter(nil)
	t.Cleanup(func() { SetErrorReporter(nil) })

	reporter := &testErrorReporter{}
	SetErrorReporter(reporter)

	require.NotPanics(t, func() {
		reportPanicToErrorService(
			nil,
			"test panic",
			[]byte("stack"),
			"component",
			"goroutine",
		) //nolint:staticcheck // Testing nil context intentionally
	})

	require.NotNil(t, reporter.getCapturedErr())
	assert.Contains(t, reporter.getCapturedErr().Error(), "test panic")
}

// TestReportPanicToErrorService_NilStackTrace tests reporting with nil stack trace.
//
//nolint:paralleltest // Cannot use t.Parallel() - modifies global errorReporterInstance
func TestReportPanicToErrorService_NilStackTrace(t *testing.T) {
	SetErrorReporter(nil)
	t.Cleanup(func() { SetErrorReporter(nil) })

	reporter := &testErrorReporter{}
	SetErrorReporter(reporter)

	reportPanicToErrorService(context.Background(), "test panic", nil, "component", "goroutine")

	tags := reporter.getCapturedTags()
	require.NotNil(t, tags)
	_, hasStackTrace := tags["stack_trace"]
	assert.False(t, hasStackTrace, "Should not include stack_trace tag when stack is nil")
}

// TestReportPanicToErrorService_EmptyStackTrace tests reporting with empty stack trace.
//
//nolint:paralleltest // Cannot use t.Parallel() - modifies global errorReporterInstance
func TestReportPanicToErrorService_EmptyStackTrace(t *testing.T) {
	SetErrorReporter(nil)
	t.Cleanup(func() { SetErrorReporter(nil) })

	reporter := &testErrorReporter{}
	SetErrorReporter(reporter)

	reportPanicToErrorService(
		context.Background(),
		"test panic",
		[]byte{},
		"component",
		"goroutine",
	)

	tags := reporter.getCapturedTags()
	require.NotNil(t, tags)
	_, hasStackTrace := tags["stack_trace"]
	assert.False(t, hasStackTrace, "Should not include stack_trace tag when stack is empty")
}

// TestReportPanicToErrorService_StackTraceTruncation tests that long stack traces are truncated.
//
//nolint:paralleltest // Cannot use t.Parallel() - modifies global errorReporterInstance
func TestReportPanicToErrorService_StackTraceTruncation(t *testing.T) {
	SetErrorReporter(nil)
	t.Cleanup(func() { SetErrorReporter(nil) })

	reporter := &testErrorReporter{}
	SetErrorReporter(reporter)

	longStack := strings.Repeat("a", 5000)
	reportPanicToErrorService(
		context.Background(),
		"test panic",
		[]byte(longStack),
		"component",
		"goroutine",
	)

	tags := reporter.getCapturedTags()
	require.NotNil(t, tags)

	stackTrace, hasStackTrace := tags["stack_trace"]
	require.True(t, hasStackTrace)
	assert.True(t, strings.HasSuffix(stackTrace, "...[truncated]"))
	assert.LessOrEqual(t, len(stackTrace), 4096+len("\n...[truncated]"))
}

// TestReportPanicToErrorService_StackTraceExactlyMaxLen tests stack trace at exactly max length.
//
//nolint:paralleltest // Cannot use t.Parallel() - modifies global errorReporterInstance
func TestReportPanicToErrorService_StackTraceExactlyMaxLen(t *testing.T) {
	SetErrorReporter(nil)
	t.Cleanup(func() { SetErrorReporter(nil) })

	reporter := &testErrorReporter{}
	SetErrorReporter(reporter)

	exactStack := strings.Repeat("a", 4096)
	reportPanicToErrorService(
		context.Background(),
		"test panic",
		[]byte(exactStack),
		"component",
		"goroutine",
	)

	tags := reporter.getCapturedTags()
	require.NotNil(t, tags)

	stackTrace, hasStackTrace := tags["stack_trace"]
	require.True(t, hasStackTrace)
	assert.False(
		t,
		strings.HasSuffix(stackTrace, "...[truncated]"),
		"Should not truncate at exactly max length",
	)
	assert.Equal(t, exactStack, stackTrace)
}

// TestReportPanicToErrorService_PanicValueTypes tests different panic value types.
//
//nolint:paralleltest // Cannot use t.Parallel() - modifies global errorReporterInstance
func TestReportPanicToErrorService_PanicValueTypes(t *testing.T) {
	tests := []struct {
		name           string
		panicValue     any
		expectedSubstr string
	}{
		{
			name:           "error type",
			panicValue:     errPanicError,
			expectedSubstr: "error panic",
		},
		{
			name:           "string type",
			panicValue:     "string panic",
			expectedSubstr: "string panic",
		},
		{
			name:           "int type",
			panicValue:     42,
			expectedSubstr: "panic: 42",
		},
		{
			name:           "struct type",
			panicValue:     struct{ Field string }{Field: "value"},
			expectedSubstr: "panic: {value}",
		},
		{
			name:           "nil value",
			panicValue:     nil,
			expectedSubstr: "panic: <nil>",
		},
		{
			name:           "slice type",
			panicValue:     []int{1, 2, 3},
			expectedSubstr: "panic: [1 2 3]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SetErrorReporter(nil)
			t.Cleanup(func() { SetErrorReporter(nil) })

			reporter := &testErrorReporter{}
			SetErrorReporter(reporter)

			reportPanicToErrorService(
				context.Background(),
				tt.panicValue,
				[]byte("stack"),
				"component",
				"goroutine",
			)

			err := reporter.getCapturedErr()
			require.NotNil(t, err)
			assert.Contains(t, err.Error(), tt.expectedSubstr)
		})
	}
}

// TestReportPanicToErrorService_Tags tests that all expected tags are set.
//
//nolint:paralleltest // Cannot use t.Parallel() - modifies global errorReporterInstance
func TestReportPanicToErrorService_Tags(t *testing.T) {
	SetErrorReporter(nil)
	t.Cleanup(func() { SetErrorReporter(nil) })

	reporter := &testErrorReporter{}
	SetErrorReporter(reporter)

	reportPanicToErrorService(
		context.Background(),
		"test",
		[]byte("stack"),
		"my-component",
		"my-goroutine",
	)

	tags := reporter.getCapturedTags()
	require.NotNil(t, tags)
	assert.Equal(t, "my-component", tags["component"])
	assert.Equal(t, "my-goroutine", tags["goroutine_name"])
	assert.Equal(t, "recovered", tags["panic_type"])
	assert.Equal(t, "stack", tags["stack_trace"])
}

// TestFormatPanicValue tests formatPanicValue with various input types.
func TestFormatPanicValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		value    any
		expected string
	}{
		{
			name:     "nil value",
			value:    nil,
			expected: "<nil>",
		},
		{
			name:     "string value",
			value:    "test string",
			expected: "test string",
		},
		{
			name:     "error value",
			value:    errTestError,
			expected: "test error",
		},
		{
			name:     "int value",
			value:    123,
			expected: "123",
		},
		{
			name:     "float value",
			value:    3.14,
			expected: "3.14",
		},
		{
			name:     "bool value",
			value:    true,
			expected: "true",
		},
		{
			name:     "struct value",
			value:    struct{ Name string }{Name: "test"},
			expected: "{test}",
		},
		{
			name:     "slice value",
			value:    []string{"a", "b"},
			expected: "[a b]",
		},
		{
			name:     "map value",
			value:    map[string]int{"key": 1},
			expected: "map[key:1]",
		},
		{
			name:     "empty string",
			value:    "",
			expected: "",
		},
		{
			name:     "pointer to int",
			value:    func() any { i := 42; return &i }(),
			expected: "", // Will be a pointer address, just check it doesn't panic
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := formatPanicValue(tt.value)

			if tt.name == "pointer to int" {
				assert.NotEmpty(t, result)
			} else {
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestConcurrentSetGetErrorReporter tests thread safety of SetErrorReporter/GetErrorReporter.
//
//nolint:paralleltest // Cannot use t.Parallel() - modifies global errorReporterInstance
func TestConcurrentSetGetErrorReporter(t *testing.T) {
	SetErrorReporter(nil)
	t.Cleanup(func() { SetErrorReporter(nil) })

	const (
		goroutines = 100
		iterations = 100
	)

	var wg sync.WaitGroup

	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()

			for j := 0; j < iterations; j++ {
				reporter := &testErrorReporter{}
				SetErrorReporter(reporter)
			}
		}()

		go func() {
			defer wg.Done()

			for j := 0; j < iterations; j++ {
				_ = GetErrorReporter()
			}
		}()
	}

	wg.Wait()
}

// TestConcurrentReportPanic tests thread safety of reportPanicToErrorService.
//
//nolint:paralleltest // Cannot use t.Parallel() - modifies global errorReporterInstance
func TestConcurrentReportPanic(t *testing.T) {
	SetErrorReporter(nil)
	t.Cleanup(func() { SetErrorReporter(nil) })

	reporter := &testErrorReporter{}
	SetErrorReporter(reporter)

	const goroutines = 50

	var wg sync.WaitGroup

	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()

			reportPanicToErrorService(
				context.Background(),
				fmt.Sprintf("panic %d", id),
				[]byte("stack"),
				"component",
				fmt.Sprintf("goroutine-%d", id),
			)
		}(i)
	}

	wg.Wait()

	assert.Equal(t, goroutines, reporter.getCallCount())
}

// TestReportPanicToErrorService_WrappedError tests that wrapped errors are handled correctly.
//
//nolint:paralleltest // Cannot use t.Parallel() - modifies global errorReporterInstance
func TestReportPanicToErrorService_WrappedError(t *testing.T) {
	SetErrorReporter(nil)
	t.Cleanup(func() { SetErrorReporter(nil) })

	reporter := &testErrorReporter{}
	SetErrorReporter(reporter)

	wrappedErr := fmt.Errorf("wrapped: %w", errBasePanic)

	reportPanicToErrorService(
		context.Background(),
		wrappedErr,
		[]byte("stack"),
		"component",
		"goroutine",
	)

	capturedErr := reporter.getCapturedErr()
	require.NotNil(t, capturedErr)
	assert.Equal(t, wrappedErr, capturedErr)
	assert.True(t, errors.Is(capturedErr, errBasePanic))
}

// TestFormatPanicValue_CustomStringer tests formatPanicValue with a custom Stringer.
func TestFormatPanicValue_CustomStringer(t *testing.T) {
	t.Parallel()

	stringer := struct {
		value string
	}{value: "custom"}

	result := formatPanicValue(stringer)
	assert.Equal(t, "{custom}", result)
}

// TestFormatPanicValue_CustomError tests formatPanicValue with a custom error type.
func TestFormatPanicValue_CustomError(t *testing.T) {
	t.Parallel()

	type customError struct {
		code int
		msg  string
	}

	customErr := &customError{code: 500, msg: "internal error"}

	result := formatPanicValue(customErr)
	assert.Contains(t, result, "500")
	assert.Contains(t, result, "internal error")
}

// TestSetProductionMode tests enabling and disabling production mode.
//
//nolint:paralleltest // Cannot use t.Parallel() - modifies global productionMode
func TestSetProductionMode(t *testing.T) {
	SetProductionMode(false)
	t.Cleanup(func() { SetProductionMode(false) })

	assert.False(t, IsProductionMode())

	SetProductionMode(true)
	assert.True(t, IsProductionMode())

	SetProductionMode(false)
	assert.False(t, IsProductionMode())
}

// TestReportPanicToErrorService_ProductionMode_RedactsPanicDetails tests that production mode redacts panic values.
//
//nolint:paralleltest // Cannot use t.Parallel() - modifies global state
func TestReportPanicToErrorService_ProductionMode_RedactsPanicDetails(t *testing.T) {
	SetErrorReporter(nil)
	SetProductionMode(false)
	t.Cleanup(func() {
		SetErrorReporter(nil)
		SetProductionMode(false)
	})

	SetProductionMode(true)

	reporter := &testErrorReporter{}
	SetErrorReporter(reporter)

	reportPanicToErrorService(
		context.Background(),
		errSensitiveDetails,
		[]byte("stack"),
		"component",
		"goroutine",
	)

	capturedErr := reporter.getCapturedErr()
	require.NotNil(t, capturedErr)
	assert.Equal(t, "panic recovered (details redacted)", capturedErr.Error())
	assert.NotContains(t, capturedErr.Error(), "secret123")
}

// TestReportPanicToErrorService_ProductionMode_RedactsStackTrace tests that production mode omits stack traces.
//
//nolint:paralleltest // Cannot use t.Parallel() - modifies global state
func TestReportPanicToErrorService_ProductionMode_RedactsStackTrace(t *testing.T) {
	SetErrorReporter(nil)
	SetProductionMode(false)
	t.Cleanup(func() {
		SetErrorReporter(nil)
		SetProductionMode(false)
	})

	SetProductionMode(true)

	reporter := &testErrorReporter{}
	SetErrorReporter(reporter)

	reportPanicToErrorService(
		context.Background(),
		"test panic",
		[]byte("sensitive stack trace here"),
		"component",
		"goroutine",
	)

	tags := reporter.getCapturedTags()
	require.NotNil(t, tags)
	_, hasStackTrace := tags["stack_trace"]
	assert.False(t, hasStackTrace, "Production mode should not include stack_trace")
}

// TestReportPanicToErrorService_NonProductionMode_IncludesDetails tests that non-production mode includes full details.
//
//nolint:paralleltest // Cannot use t.Parallel() - modifies global state
func TestReportPanicToErrorService_NonProductionMode_IncludesDetails(t *testing.T) {
	SetErrorReporter(nil)
	SetProductionMode(false)
	t.Cleanup(func() {
		SetErrorReporter(nil)
		SetProductionMode(false)
	})

	reporter := &testErrorReporter{}
	SetErrorReporter(reporter)

	reportPanicToErrorService(
		context.Background(),
		errDetailedMessage,
		[]byte("full stack trace"),
		"component",
		"goroutine",
	)

	capturedErr := reporter.getCapturedErr()
	require.NotNil(t, capturedErr)
	assert.Equal(t, errDetailedMessage, capturedErr)

	tags := reporter.getCapturedTags()
	require.NotNil(t, tags)
	stackTrace, hasStackTrace := tags["stack_trace"]
	assert.True(t, hasStackTrace, "Non-production mode should include stack_trace")
	assert.Equal(t, "full stack trace", stackTrace)
}

// TestConcurrentSetProductionMode tests thread safety of SetProductionMode/IsProductionMode.
//
//nolint:paralleltest // Cannot use t.Parallel() - modifies global productionMode
func TestConcurrentSetProductionMode(t *testing.T) {
	SetProductionMode(false)
	t.Cleanup(func() { SetProductionMode(false) })

	const (
		goroutines = 100
		iterations = 100
	)

	var wg sync.WaitGroup

	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()

			for j := 0; j < iterations; j++ {
				SetProductionMode(id%2 == 0)
			}
		}(i)

		go func() {
			defer wg.Done()

			for j := 0; j < iterations; j++ {
				_ = IsProductionMode()
			}
		}()
	}

	wg.Wait()
}
