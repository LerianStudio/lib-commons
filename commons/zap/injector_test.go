package zap

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInitializeLogger(t *testing.T) {
	_ = os.Setenv("ENV_NAME", "production")
	defer func() { _ = os.Unsetenv("ENV_NAME") }()
	logger := InitializeLogger()
	assert.NotNil(t, logger)
}
