//go:build unit

package rabbitmq

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSanitizedError_Unwrap covers the Unwrap method.
func TestRabbitSanitizedError_Unwrap(t *testing.T) {
	t.Parallel()

	inner := errors.New("original rabbitmq error")
	se := &sanitizedError{
		original: inner,
		message:  "sanitized message",
	}

	result := se.Unwrap()
	require.NotNil(t, result)
	assert.Equal(t, inner, result)
}

func TestRabbitSanitizedError_Unwrap_Nil(t *testing.T) {
	t.Parallel()

	se := &sanitizedError{
		original: nil,
		message:  "no original",
	}

	result := se.Unwrap()
	assert.Nil(t, result)
}

// TestSanitizedError_Error covers the Error() method.
func TestRabbitSanitizedError_Error(t *testing.T) {
	t.Parallel()

	se := &sanitizedError{message: "rabbitmq connection error: ***"}
	assert.Equal(t, "rabbitmq connection error: ***", se.Error())
}
