//go:build unit

package server_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v6/commons/server"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	_ "github.com/stretchr/testify/require"
)

// -------------------------------------------------------------------
// handleShutdown — via shutdownChan (exercises shutdownChan path)
// -------------------------------------------------------------------

func TestStartWithGracefulShutdownWithError_ShutdownViaChannel(t *testing.T) {
	t.Parallel()

	logger := &recordingLogger{}
	app := fiber.New()

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
	assert.NoError(t, err)
}

// -------------------------------------------------------------------
// ensureRuntimeDefaults — covered by calling Start with nil logger
// -------------------------------------------------------------------

func TestStartWithGracefulShutdownWithError_NilLogger(t *testing.T) {
	t.Parallel()

	app := fiber.New()
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
	assert.NoError(t, err)
}

// -------------------------------------------------------------------
// ServersStarted — returns readable channel after start
// -------------------------------------------------------------------

func TestServersStarted_ReturnsChannel(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	shutdownCh := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(app, ":0").
		WithShutdownChannel(shutdownCh).
		WithShutdownTimeout(50 * time.Millisecond)

	ch := sm.ServersStarted()
	assert.NotNil(t, ch)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// The server goroutine must be joined before the test returns: Fiber prints its
	// startup banner from that goroutine AFTER signalling ServersStarted, and a
	// goroutine that outlives the test races with the testing package swapping
	// os.Stdout for the Example runs ("race detected outside of test execution").
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = sm.StartWithGracefulShutdownWithError()
	}()

	// Every exit of this test, including a t.Fatal on the timeout branches below,
	// must stop the server and join its goroutine, or the leak outlives the test.
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(shutdownCh) }) }
	t.Cleanup(func() {
		stop()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})

	select {
	case <-ch:
		// started was signaled
	case <-ctx.Done():
		t.Fatal("ServersStarted did not signal startup before timeout")
	}

	stop()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("server goroutine did not return after shutdown before timeout")
	}
}

// -------------------------------------------------------------------
// WithShutdownHook — registers and calls hook on shutdown
// -------------------------------------------------------------------

func TestWithShutdownHook_IsCalledOnShutdown(t *testing.T) {
	t.Parallel()

	hookCalled := false
	app := fiber.New()
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

	app := fiber.New()
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

	app := fiber.New()
	// Port :1 is typically blocked; use a dynamic approach: bind to :0 twice
	// Actually, start a server on :0 and get its actual port
	app2 := fiber.New()
	shutdownCh := make(chan struct{})
	defer close(shutdownCh)

	sm := server.NewServerManager(nil, nil, logger).
		WithHTTPServer(app, ":99999"). // invalid port - will fail
		WithHTTPServer(app2, ":99998").
		WithShutdownChannel(shutdownCh).
		WithShutdownTimeout(200 * time.Millisecond)

	// Should return an error because the port is invalid
	err := sm.StartWithGracefulShutdownWithError()
	assert.Error(t, err)
}
