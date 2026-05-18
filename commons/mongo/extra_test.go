//go:build unit

package mongo

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSanitizedError_Unwrap covers the Unwrap method.
func TestSanitizedError_Unwrap(t *testing.T) {
	t.Parallel()

	inner := errors.New("inner cause")
	se := &SanitizedError{
		Message: "sanitized message",
		cause:   inner,
	}

	result := se.Unwrap()
	require.NotNil(t, result)
	assert.Equal(t, inner, result)
}

func TestSanitizedError_Unwrap_Nil(t *testing.T) {
	t.Parallel()

	se := &SanitizedError{
		Message: "no cause",
		cause:   nil,
	}

	result := se.Unwrap()
	assert.Nil(t, result)
}

// TestSanitizedError_Error covers the Error() method.
func TestSanitizedError_Error(t *testing.T) {
	t.Parallel()

	se := &SanitizedError{Message: "test message"}
	assert.Equal(t, "test message", se.Error())
}
