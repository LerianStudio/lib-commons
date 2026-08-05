//go:build unit

// This file is deliberately in package pacing rather than pacing_test: the reply
// guards below cannot be reached through a Redis backend, because the script
// this package ships never produces those shapes. They exist for a
// Redis-compatible backend that answers differently, and the only honest way to
// assert they fail closed is to call the parser directly.
package pacing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEvaluation_FailsClosedOnEveryUnexpectedShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result []any
	}{
		{"empty reply", []any{}},
		{"one value", []any{int64(1)}},
		{"three values", []any{int64(1), int64(0), int64(0)}},
		{"grant flag is a string", []any{"1", int64(0)}},
		{"grant flag is nil", []any{nil, int64(0)}},
		{"wait is a string", []any{int64(0), "5000"}},
		{"unknown grant code", []any{int64(7), int64(5000)}},
		{"negative grant code", []any{int64(-1), int64(5000)}},
		{"refusal without a wait", []any{int64(0), int64(0)}},
		{"refusal with a negative wait", []any{int64(0), int64(-1)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			granted, retryAfter, err := parseEvaluation(tt.result)

			require.ErrorIs(t, err, ErrBackendUnavailable,
				"an uninterpretable reply must refuse and say the backend is unusable")
			assert.False(t, granted, "no permit may be issued on an uninterpretable reply")
			assert.Zero(t, retryAfter, "a refusal carries no wait")
		})
	}
}

func TestParseEvaluation_AcceptsTheTwoShapesTheScriptProduces(t *testing.T) {
	t.Parallel()

	granted, retryAfter, err := parseEvaluation([]any{int64(1), int64(0)})
	require.NoError(t, err)
	assert.True(t, granted)
	assert.Zero(t, retryAfter)

	granted, retryAfter, err = parseEvaluation([]any{int64(0), int64(5000)})
	require.NoError(t, err)
	assert.False(t, granted)
	assert.Equal(t, 5*time.Millisecond, retryAfter, "the wait is reported in microseconds")
}
