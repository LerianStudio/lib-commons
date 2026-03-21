//go:build unit

package circuitbreaker

import (
	"testing"

	"github.com/sony/gobreaker"
	"github.com/stretchr/testify/assert"
)

func TestConfig_Validate_BothTripConditionsZero(t *testing.T) {
	cfg := Config{
		ConsecutiveFailures: 0,
		MinRequests:         0,
	}

	err := cfg.Validate()
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidConfig)
	assert.Contains(t, err.Error(), "at least one trip condition must be set")
}

func TestConfig_Validate_InvalidFailureRatio_Negative(t *testing.T) {
	cfg := Config{
		ConsecutiveFailures: 5,
		FailureRatio:        -0.1,
	}

	err := cfg.Validate()
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidConfig)
	assert.Contains(t, err.Error(), "FailureRatio must be between 0 and 1")
}

func TestConfig_Validate_InvalidFailureRatio_GreaterThanOne(t *testing.T) {
	cfg := Config{
		ConsecutiveFailures: 5,
		FailureRatio:        1.1,
	}

	err := cfg.Validate()
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidConfig)
	assert.Contains(t, err.Error(), "FailureRatio must be between 0 and 1")
}

func TestConfig_Validate_ValidConfig(t *testing.T) {
	cfg := Config{
		ConsecutiveFailures: 5,
		FailureRatio:        0.5,
		MinRequests:         10,
	}

	err := cfg.Validate()
	assert.NoError(t, err)
}

func TestConfig_Validate_OnlyConsecutiveFailuresSet(t *testing.T) {
	cfg := Config{
		ConsecutiveFailures: 3,
		MinRequests:         0,
		FailureRatio:        0,
	}

	err := cfg.Validate()
	assert.NoError(t, err)
}

func TestConfig_Validate_MinRequestsAndFailureRatioSet(t *testing.T) {
	cfg := Config{
		ConsecutiveFailures: 0,
		MinRequests:         10,
		FailureRatio:        0.5,
	}

	err := cfg.Validate()
	assert.NoError(t, err)
}

func TestConfig_Validate_BoundaryFailureRatio(t *testing.T) {
	// FailureRatio = 0 is valid
	cfg := Config{
		ConsecutiveFailures: 5,
		FailureRatio:        0,
	}
	assert.NoError(t, cfg.Validate())

	// FailureRatio = 1 is valid
	cfg.FailureRatio = 1
	assert.NoError(t, cfg.Validate())
}

func TestConvertGobreakerState_Closed(t *testing.T) {
	assert.Equal(t, StateClosed, convertGobreakerState(gobreaker.StateClosed))
}

func TestConvertGobreakerState_Open(t *testing.T) {
	assert.Equal(t, StateOpen, convertGobreakerState(gobreaker.StateOpen))
}

func TestConvertGobreakerState_HalfOpen(t *testing.T) {
	assert.Equal(t, StateHalfOpen, convertGobreakerState(gobreaker.StateHalfOpen))
}

func TestConvertGobreakerState_Unknown(t *testing.T) {
	// Use an arbitrary value that doesn't map to any known state
	assert.Equal(t, StateUnknown, convertGobreakerState(gobreaker.State(99)))
}
