//go:build unit

package log

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSafeError_ProductionAndNonProduction(t *testing.T) {
	var buf bytes.Buffer
	withTestLoggerOutput(t, &buf)

	logger := &GoLogger{Level: LevelDebug}
	err := errors.New("credential_id=abc123")

	SafeError(logger, context.Background(), "request failed", err, false)
	assert.Contains(t, buf.String(), "request failed")
	assert.Contains(t, buf.String(), "credential_id=abc123")

	buf.Reset()
	SafeError(logger, context.Background(), "request failed", err, true)
	out := buf.String()
	assert.Contains(t, out, "request failed")
	assert.Contains(t, out, "error_type=*errors.errorString")
	assert.NotContains(t, out, "credential_id=abc123")
}

func TestSafeError_NilGuards(t *testing.T) {
	t.Run("nil logger produces no output", func(t *testing.T) {
		var buf bytes.Buffer
		withTestLoggerOutput(t, &buf)

		assert.NotPanics(t, func() {
			SafeError(nil, context.Background(), "msg", assert.AnError, true)
		})
		assert.Empty(t, buf.String(), "nil logger must produce no output")
	})

	t.Run("nil error produces no output", func(t *testing.T) {
		var buf bytes.Buffer
		withTestLoggerOutput(t, &buf)

		assert.NotPanics(t, func() {
			SafeError(&GoLogger{Level: LevelInfo}, context.Background(), "msg", nil, true)
		})
		assert.Empty(t, buf.String(), "nil error must produce no output")
	})
}

func TestSanitizeExternalResponse(t *testing.T) {
	assert.Equal(t, "external system returned status 400", SanitizeExternalResponse(400))
}

// customError is a typed error for testing typed-nil behavior in SafeError.
type customError struct{ msg string }

func (e *customError) Error() string { return e.msg }

// TestSafeError_LevelFiltering verifies SafeError respects level gating.
func TestSafeError_LevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	withTestLoggerOutput(t, &buf)

	// Logger at LevelWarn should NOT emit LevelError if LevelWarn < LevelError numerically.
	// But in this codebase, LevelError=0 < LevelWarn=1, so LevelWarn logger should emit errors.
	logger := &GoLogger{Level: LevelWarn}

	SafeError(logger, context.Background(), "should appear", errors.New("err"), false)
	assert.Contains(t, buf.String(), "should appear")
}

// TestSanitizeExternalResponse_VariousCodes verifies status code formatting.
func TestSanitizeExternalResponse_VariousCodes(t *testing.T) {
	tests := []struct {
		code     int
		expected string
	}{
		{200, "external system returned status 200"},
		{400, "external system returned status 400"},
		{401, "external system returned status 401"},
		{403, "external system returned status 403"},
		{404, "external system returned status 404"},
		{500, "external system returned status 500"},
		{502, "external system returned status 502"},
		{503, "external system returned status 503"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.expected, SanitizeExternalResponse(tt.code))
	}
}
