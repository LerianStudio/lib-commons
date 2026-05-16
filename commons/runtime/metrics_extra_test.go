//go:build unit

package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestResetPanicMetrics covers the ResetPanicMetrics function.
func TestResetPanicMetrics_DoesNotPanic(t *testing.T) {
	t.Parallel()

	assert.NotPanics(t, func() {
		ResetPanicMetrics()
	})
}

// TestGetPanicMetrics_AfterReset covers GetPanicMetrics after reset.
func TestGetPanicMetrics_AfterReset(t *testing.T) {
	t.Parallel()

	// Reset should be safe to call
	ResetPanicMetrics()

	// GetPanicMetrics should return nil after reset
	result := GetPanicMetrics()
	// Returns nil since we didn't initialize with a factory
	assert.Nil(t, result)
}
