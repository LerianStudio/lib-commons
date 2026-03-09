//go:build unit

package log

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// ---------------------------------------------------------------------------
// CWE-117: Comprehensive sanitizeLogString test matrix
// ---------------------------------------------------------------------------

func TestSanitizeLogString_ControlCharacterMatrix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		assertFn func(t *testing.T, result string)
	}{
		// --- Newline variants (MUST be neutralized for CWE-117) ---
		{
			name:  "LF newline is escaped",
			input: "line1\nline2",
			assertFn: func(t *testing.T, result string) {
				t.Helper()
				assert.NotContains(t, result, "\n")
				assert.Contains(t, result, `\n`)
			},
		},
		{
			name:  "CR carriage return is escaped",
			input: "line1\rline2",
			assertFn: func(t *testing.T, result string) {
				t.Helper()
				assert.NotContains(t, result, "\r")
				assert.Contains(t, result, `\r`)
			},
		},
		{
			name:  "CRLF is escaped",
			input: "line1\r\nline2",
			assertFn: func(t *testing.T, result string) {
				t.Helper()
				assert.NotContains(t, result, "\r")
				assert.NotContains(t, result, "\n")
				assert.Contains(t, result, `\r`)
				assert.Contains(t, result, `\n`)
			},
		},
		{
			name:  "tab character is escaped",
			input: "field1\tfield2",
			assertFn: func(t *testing.T, result string) {
				t.Helper()
				assert.NotContains(t, result, "\t")
				assert.Contains(t, result, `\t`)
			},
		},

		// --- Null bytes ---
		{
			name:  "null byte is removed or escaped",
			input: "before\x00after",
			assertFn: func(t *testing.T, result string) {
				t.Helper()
				// The sanitizer should at minimum not pass through raw null bytes.
				// Depending on implementation it may remove or escape them.
				assert.NotContains(t, result, "\x00")
			},
		},

		// --- Normal strings (pass-through) ---
		{
			name:  "normal ASCII passes through unchanged",
			input: "hello world 123 !@#$%",
			assertFn: func(t *testing.T, result string) {
				t.Helper()
				assert.Equal(t, "hello world 123 !@#$%", result)
			},
		},
		{
			name:  "empty string passes through",
			input: "",
			assertFn: func(t *testing.T, result string) {
				t.Helper()
				assert.Equal(t, "", result)
			},
		},
		{
			name:  "legitimate Unicode text passes through",
			input: "Hello, \u4e16\u754c! Ol\u00e1! \u00dcber!",
			assertFn: func(t *testing.T, result string) {
				t.Helper()
				// Normal Unicode should be preserved
				assert.Contains(t, result, "\u4e16\u754c")
				assert.Contains(t, result, "Ol\u00e1")
			},
		},

		// --- Multiple embedded control chars ---
		{
			name:  "multiple newlines in single string",
			input: "line1\nline2\nline3\nline4",
			assertFn: func(t *testing.T, result string) {
				t.Helper()
				assert.NotContains(t, result, "\n")
				// All 3 newlines should be escaped
				assert.Equal(t, 3, strings.Count(result, `\n`))
			},
		},
		{
			name:  "mixed control characters",
			input: "start\nmiddle\rend\ttab",
			assertFn: func(t *testing.T, result string) {
				t.Helper()
				assert.NotContains(t, result, "\n")
				assert.NotContains(t, result, "\r")
				assert.NotContains(t, result, "\t")
			},
		},

		// --- Very long strings ---
		{
			name:  "very long string with embedded control chars",
			input: strings.Repeat("a", 5000) + "\n" + strings.Repeat("b", 5000),
			assertFn: func(t *testing.T, result string) {
				t.Helper()
				assert.NotContains(t, result, "\n")
				assert.Contains(t, result, `\n`)
				// Verify content integrity: the 'a's and 'b's should still be there
				assert.True(t, len(result) > 10000)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeLogString(tt.input)
			tt.assertFn(t, result)
		})
	}
}

// TestSanitizeFieldValue_TypeDispatch verifies the sanitizeFieldValue function
// correctly handles both string and non-string values.
func TestSanitizeFieldValue_TypeDispatch(t *testing.T) {
	t.Run("string values are sanitized", func(t *testing.T) {
		result := sanitizeFieldValue("value\ninjected")
		s, ok := result.(string)
		require.True(t, ok)
		assert.NotContains(t, s, "\n")
		assert.Contains(t, s, `\n`)
	})

	t.Run("integer values pass through", func(t *testing.T) {
		result := sanitizeFieldValue(42)
		assert.Equal(t, 42, result)
	})

	t.Run("boolean values pass through", func(t *testing.T) {
		result := sanitizeFieldValue(true)
		assert.Equal(t, true, result)
	})

	t.Run("nil values pass through", func(t *testing.T) {
		result := sanitizeFieldValue(nil)
		assert.Nil(t, result)
	})

	t.Run("error values are sanitized", func(t *testing.T) {
		err := errors.New("some error\nwith newline")
		result := sanitizeFieldValue(err)
		s, ok := result.(string)
		require.True(t, ok, "error values should be converted to sanitized strings")
		assert.NotContains(t, s, "\n")
		assert.Contains(t, s, `\n`)
		assert.Equal(t, `some error\nwith newline`, s)
	})

	t.Run("struct values with newlines are sanitized", func(t *testing.T) {
		type payload struct {
			Msg string
		}
		result := sanitizeFieldValue(payload{Msg: "line1\nline2"})
		s, ok := result.(string)
		require.True(t, ok, "composite types should be serialized to sanitized strings")
		assert.NotContains(t, s, "\n")
	})

	t.Run("slice values with newlines are sanitized", func(t *testing.T) {
		result := sanitizeFieldValue([]string{"a\nb", "c"})
		s, ok := result.(string)
		require.True(t, ok, "slices should be serialized to sanitized strings")
		assert.NotContains(t, s, "\n")
	})

	t.Run("map values with newlines are sanitized", func(t *testing.T) {
		result := sanitizeFieldValue(map[string]string{"k": "v\ninjected"})
		s, ok := result.(string)
		require.True(t, ok, "maps should be serialized to sanitized strings")
		assert.NotContains(t, s, "\n")
	})

	t.Run("typed-nil error returns placeholder", func(t *testing.T) {
		var err *customError // typed nil
		result := sanitizeFieldValue(err)
		assert.Equal(t, "<nil>", result,
			"typed-nil error should return placeholder, not panic")
	})

	t.Run("typed-nil stringer returns placeholder", func(t *testing.T) {
		var s *testStringer // typed nil
		result := sanitizeFieldValue(s)
		assert.Equal(t, "<nil>", result,
			"typed-nil Stringer should return placeholder, not panic")
	})
}

// customError is a typed error for testing typed-nil behavior.
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
