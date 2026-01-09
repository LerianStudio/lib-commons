package circuitbreaker

import (
	"errors"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v2/commons/log"
	"github.com/stretchr/testify/assert"
)

func TestNewHealthCheckerWithValidation_Success(t *testing.T) {
	logger := &log.NoneLogger{}
	manager := NewManager(logger)

	hc, err := NewHealthCheckerWithValidation(manager, 1*time.Second, 500*time.Millisecond, logger)

	assert.NoError(t, err)
	assert.NotNil(t, hc)
}

func TestNewHealthCheckerWithValidation_InvalidInterval(t *testing.T) {
	logger := &log.NoneLogger{}
	manager := NewManager(logger)

	hc, err := NewHealthCheckerWithValidation(manager, 0, 500*time.Millisecond, logger)

	assert.Nil(t, hc)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidHealthCheckInterval))
}

func TestNewHealthCheckerWithValidation_NegativeInterval(t *testing.T) {
	logger := &log.NoneLogger{}
	manager := NewManager(logger)

	hc, err := NewHealthCheckerWithValidation(manager, -1*time.Second, 500*time.Millisecond, logger)

	assert.Nil(t, hc)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidHealthCheckInterval))
}

func TestNewHealthCheckerWithValidation_InvalidTimeout(t *testing.T) {
	logger := &log.NoneLogger{}
	manager := NewManager(logger)

	hc, err := NewHealthCheckerWithValidation(manager, 1*time.Second, 0, logger)

	assert.Nil(t, hc)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidHealthCheckTimeout))
}

func TestNewHealthCheckerWithValidation_NegativeTimeout(t *testing.T) {
	logger := &log.NoneLogger{}
	manager := NewManager(logger)

	hc, err := NewHealthCheckerWithValidation(manager, 1*time.Second, -500*time.Millisecond, logger)

	assert.Nil(t, hc)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidHealthCheckTimeout))
}

func TestNewHealthChecker_PanicOnInvalidInterval(t *testing.T) {
	logger := &log.NoneLogger{}
	manager := NewManager(logger)

	assert.Panics(t, func() {
		NewHealthChecker(manager, 0, 500*time.Millisecond, logger)
	})
}

func TestNewHealthChecker_PanicOnInvalidTimeout(t *testing.T) {
	logger := &log.NoneLogger{}
	manager := NewManager(logger)

	assert.Panics(t, func() {
		NewHealthChecker(manager, 1*time.Second, 0, logger)
	})
}

func TestNewHealthChecker_Success(t *testing.T) {
	logger := &log.NoneLogger{}
	manager := NewManager(logger)

	hc := NewHealthChecker(manager, 1*time.Second, 500*time.Millisecond, logger)

	assert.NotNil(t, hc)
}
