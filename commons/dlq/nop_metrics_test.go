//go:build unit

package dlq

import (
	"context"
	"errors"
	"github.com/LerianStudio/lib-commons/v6/commons/obs"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNopDLQMetrics covers the nop metric methods.
func TestNopDLQMetrics_RecordRetried(t *testing.T) {
	t.Parallel()

	var m nopDLQMetrics
	assert.NotPanics(t, func() {
		m.RecordRetried(context.Background(), "test-source")
	})
}

func TestNopDLQMetrics_RecordExhausted(t *testing.T) {
	t.Parallel()

	var m nopDLQMetrics
	assert.NotPanics(t, func() {
		m.RecordExhausted(context.Background(), "test-source")
	})
}

func TestNopDLQMetrics_RecordLost(t *testing.T) {
	t.Parallel()

	var m nopDLQMetrics
	assert.NotPanics(t, func() {
		m.RecordLost(context.Background(), "test-source")
	})
}

// TestLogEnqueueFallback covers the private log function.
func TestLogEnqueueFallback_DoesNotPanic(t *testing.T) {
	t.Parallel()

	h := &Handler{
		logger: obs.Nop(),
	}

	msg := &FailedMessage{
		Source:       "test-source",
		RetryCount:   2,
		ErrorMessage: "connection refused",
	}

	assert.NotPanics(t, func() {
		h.logEnqueueFallback(context.Background(), "dlq:test:test-source", msg, errors.New("redis error"))
	})
}
