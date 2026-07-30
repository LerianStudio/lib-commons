//go:build unit

package server

import (
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithHTTPServerLeavesListenConfigUnset(t *testing.T) {
	t.Parallel()

	sm := NewServerManager(nil, nil, nil).WithHTTPServer(fiber.New(), ":0")

	assert.Nil(t, sm.httpListenConfig,
		"WithHTTPServer must leave the listen config unset so Listen is called with zero variadic arguments, exactly as before")
}

func TestWithHTTPServerConfigStoresIndependentCopy(t *testing.T) {
	t.Parallel()

	cfg := fiber.ListenConfig{DisableStartupMessage: true, ListenerNetwork: fiber.NetworkTCP4}

	sm := NewServerManager(nil, nil, nil).WithHTTPServerConfig(fiber.New(), ":0", cfg)

	require.NotNil(t, sm.httpListenConfig)
	assert.True(t, sm.httpListenConfig.DisableStartupMessage)
	assert.Equal(t, fiber.NetworkTCP4, sm.httpListenConfig.ListenerNetwork)

	cfg.DisableStartupMessage = false
	cfg.ListenerNetwork = fiber.NetworkTCP6

	assert.True(t, sm.httpListenConfig.DisableStartupMessage,
		"the stored listen config must not alias the caller's value")
	assert.Equal(t, fiber.NetworkTCP4, sm.httpListenConfig.ListenerNetwork)
}

func TestWithHTTPServerConfigOverwritesPreviousUnsetState(t *testing.T) {
	t.Parallel()

	app := fiber.New()

	sm := NewServerManager(nil, nil, nil).
		WithHTTPServer(app, ":0").
		WithHTTPServerConfig(app, "127.0.0.1:0", fiber.ListenConfig{DisableStartupMessage: true})

	require.NotNil(t, sm.httpListenConfig)
	assert.Equal(t, "127.0.0.1:0", sm.httpAddress)
	assert.Same(t, app, sm.httpServer)
}
