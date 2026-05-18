//go:build unit

package commons

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGetenvFloat64OrDefault covers the float64 env var parsing.
func TestGetenvFloat64OrDefault_NotSet(t *testing.T) {
	t.Parallel()

	result := GetenvFloat64OrDefault("TEST_FLOAT64_NOT_SET_XYZ", 3.14)
	assert.Equal(t, 3.14, result)
}

func TestGetenvFloat64OrDefault_ValidFloat(t *testing.T) {
	t.Setenv("TEST_FLOAT64_VALID", "2.718")
	defer os.Unsetenv("TEST_FLOAT64_VALID")

	result := GetenvFloat64OrDefault("TEST_FLOAT64_VALID", 0.0)
	assert.InDelta(t, 2.718, result, 0.001)
}

func TestGetenvFloat64OrDefault_InvalidValue(t *testing.T) {
	t.Setenv("TEST_FLOAT64_INVALID", "not-a-float")
	defer os.Unsetenv("TEST_FLOAT64_INVALID")

	result := GetenvFloat64OrDefault("TEST_FLOAT64_INVALID", 42.0)
	assert.Equal(t, 42.0, result)
}

func TestGetenvFloat64OrDefault_EmptyValue(t *testing.T) {
	t.Setenv("TEST_FLOAT64_EMPTY", "")
	defer os.Unsetenv("TEST_FLOAT64_EMPTY")

	result := GetenvFloat64OrDefault("TEST_FLOAT64_EMPTY", 99.9)
	assert.Equal(t, 99.9, result)
}
