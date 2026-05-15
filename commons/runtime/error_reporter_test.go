//go:build unit

package runtime

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testErrorReporter is a test implementation of ErrorReporter for these tests.
type testErrorReporter struct {
	mu           sync.RWMutex
	capturedErr  error
	capturedCtx  context.Context //nolint:containedctx
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
