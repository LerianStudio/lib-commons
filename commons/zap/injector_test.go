package zap

import (
	"github.com/stretchr/testify/assert"
	"os"
	"testing"
)

func TestInitializeLogger(t *testing.T) {
	_ = os.Setenv("ENV_NAME", "production")
	defer func() { _ = os.Unsetenv("ENV_NAME") }()
	logger := InitializeLogger()
	assert.NotNil(t, logger)
}
