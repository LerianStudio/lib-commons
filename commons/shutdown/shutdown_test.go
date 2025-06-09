package shutdown_test

import (
	"testing"

	"github.com/LerianStudio/lib-commons/commons/shutdown"
	"github.com/stretchr/testify/assert"
)

func TestNewGracefulShutdown(t *testing.T) {
	gs := shutdown.NewGracefulShutdown(nil, nil, nil, nil)
	assert.NotNil(t, gs, "NewGracefulShutdown should return a non-nil instance")
}
