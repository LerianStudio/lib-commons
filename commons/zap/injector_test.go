package zap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInitializeLogger(t *testing.T) {
	t.Setenv("ENV_NAME", "production")

	logger := InitializeLogger()
	assert.NotNil(t, logger)
}
