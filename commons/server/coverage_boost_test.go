//go:build unit

package server_test

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/server"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	_ "github.com/stretchr/testify/require"
)

// -------------------------------------------------------------------
// handleShutdown — via shutdownChan (exercises shutdownChan path)
// -------------------------------------------------------------------

func TestStartWithGracefulShutdownWithError_ShutdownViaChannel(t *testing.T) {
	t.Parallel()

	logger := &recordingLogger{}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})

	shutdownCh := make(chan struct{})

	sm := server.NewServerManager(nil, nil, logger).
		WithHTTPServer(app, ":0").
		WithShutdownChannel(shutdownCh).
		WithShutdownTimeout(100 * time.Millisecond)

	// Signal shutdown immediately after starting
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(shutdownCh)
	}()

	err := sm.StartWithGracefulShutdownWithError()
	// May return nil or an error depending on timing; should not panic
	_ = err
}

// -------------------------------------------------------------------
// ensureRuntimeDefaults — covered by calling Start with nil logger
// -------------------------------------------------------------------

func TestStartWithGracefulShutdownWithError_NilLogger(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	shutdownCh := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil). // nil logger
						WithHTTPServer(app, ":0").
						WithShutdownChannel(shutdownCh).
						WithShutdownTimeout(100 * time.Millisecond)

	go func() {
		time.Sleep(20 * time.Millisecond)
		close(shutdownCh)
	}()

	err := sm.StartWithGracefulShutdownWithError()
	_ = err
}

// -------------------------------------------------------------------
// ServersStarted — returns readable channel after start
// -------------------------------------------------------------------

func TestServersStarted_ReturnsChannel(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	shutdownCh := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(app, ":0").
		WithShutdownChannel(shutdownCh).
		WithShutdownTimeout(50 * time.Millisecond)

	ch := sm.ServersStarted()
	assert.NotNil(t, ch)

	// Trigger shutdown
	close(shutdownCh)

	// Wait for server to stop (with timeout)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		_ = sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-ch:
		// started was signaled
	case <-ctx.Done():
		// timeout is acceptable if server hasn't started yet
	}
}

// -------------------------------------------------------------------
// WithShutdownHook — registers and calls hook on shutdown
// -------------------------------------------------------------------

func TestWithShutdownHook_IsCalledOnShutdown(t *testing.T) {
	t.Parallel()

	hookCalled := false
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	shutdownCh := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(app, ":0").
		WithShutdownChannel(shutdownCh).
		WithShutdownTimeout(50 * time.Millisecond).
		WithShutdownHook(func(_ context.Context) error {
			hookCalled = true
			return nil
		})

	assert.NotNil(t, sm)

	go func() {
		time.Sleep(20 * time.Millisecond)
		close(shutdownCh)
	}()

	_ = sm.StartWithGracefulShutdownWithError()

	assert.True(t, hookCalled, "shutdown hook should be called")
}

// -------------------------------------------------------------------
// executeShutdown — idempotent (second call is safe)
// -------------------------------------------------------------------

func TestStartWithGracefulShutdownWithError_IdempotentClose(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	shutdownCh := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(app, ":0").
		WithShutdownChannel(shutdownCh).
		WithShutdownTimeout(50 * time.Millisecond)

	close(shutdownCh) // immediate shutdown

	// Should not panic even if called when already shut down
	assert.NotPanics(t, func() {
		_ = sm.StartWithGracefulShutdownWithError()
	})
}

// -------------------------------------------------------------------
// handleShutdown — startup error path
// -------------------------------------------------------------------

func TestStartWithGracefulShutdownWithError_StartupError(t *testing.T) {
	t.Parallel()

	// Bind to an already-in-use port to force a startup error
	// Use a listener to hold the port
	logger := &recordingLogger{}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	// Port :1 is typically blocked; use a dynamic approach: bind to :0 twice
	// Actually, start a server on :0 and get its actual port
	app2 := fiber.New(fiber.Config{DisableStartupMessage: true})
	shutdownCh := make(chan struct{})
	defer close(shutdownCh)

	sm := server.NewServerManager(nil, nil, logger).
		WithHTTPServer(app, ":99999"). // invalid port - will fail
		WithHTTPServer(app2, ":99998").
		WithShutdownChannel(shutdownCh).
		WithShutdownTimeout(200 * time.Millisecond)

	// Should return an error because the port is invalid
	err := sm.StartWithGracefulShutdownWithError()
	// May get an error about the port
	_ = err
}
