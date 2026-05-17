//go:build unit

package server_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/server"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Shutdown Hook Tests ---

func TestShutdownHook_NilFunctionIgnored(t *testing.T) {
	t.Parallel()

	sm := server.NewServerManager(nil, nil, nil)
	result := sm.WithShutdownHook(nil)

	// WithShutdownHook(nil) must return the same manager without appending.
	assert.Same(t, sm, result, "WithShutdownHook(nil) should return the same ServerManager")

	// Prove no hook was registered: run a full shutdown lifecycle and confirm
	// only the standard messages appear (no "shutdown hook failed" noise).
	logger := &recordingLogger{}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	shutdownChan := make(chan struct{})

	sm2 := server.NewServerManager(nil, nil, logger).
		WithShutdownHook(nil). // nil hook — should be silently ignored
		WithHTTPServer(app, ":0").
		WithShutdownChannel(shutdownChan)

	done := make(chan error, 1)

	go func() {
		done <- sm2.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm2.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for servers to start")
	}

	close(shutdownChan)

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for shutdown")
	}

	msgs := logger.getMessages()
	for _, msg := range msgs {
		assert.NotContains(t, msg, "shutdown hook failed",
			"no hooks should have executed when only nil was registered")
	}
}

func TestShutdownHook_NilServerManager(t *testing.T) {
	t.Parallel()

	var sm *server.ServerManager

	// Calling WithShutdownHook on a nil receiver must not panic
	// and must return nil.
	assert.NotPanics(t, func() {
		result := sm.WithShutdownHook(func(_ context.Context) error { return nil })
		assert.Nil(t, result, "WithShutdownHook on nil receiver should return nil")
	}, "WithShutdownHook on nil receiver must not panic")
}

func TestShutdownHook_StartWithGracefulShutdownWithError_NilReceiver(t *testing.T) {
	t.Parallel()

	var sm *server.ServerManager

	err := sm.StartWithGracefulShutdownWithError()
	require.ErrorIs(t, err, server.ErrNoServersConfigured,
		"nil receiver should return ErrNoServersConfigured")
}

func TestShutdownHook_ExecuteInOrder(t *testing.T) {
	t.Parallel()

	logger := &recordingLogger{}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	shutdownChan := make(chan struct{})

	// mu + order track hook execution sequence.
	var mu sync.Mutex

	var order []int

	sm := server.NewServerManager(nil, nil, logger).
		WithHTTPServer(app, ":0").
		WithShutdownChannel(shutdownChan).
		WithShutdownHook(func(_ context.Context) error {
			mu.Lock()
			defer mu.Unlock()

			order = append(order, 1)

			return nil
		}).
		WithShutdownHook(func(_ context.Context) error {
			mu.Lock()
			defer mu.Unlock()

			order = append(order, 2)

			return nil
		}).
		WithShutdownHook(func(_ context.Context) error {
			mu.Lock()
			defer mu.Unlock()

			order = append(order, 3)

			return nil
		})

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for servers to start")
	}

	close(shutdownChan)

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for shutdown")
	}

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, order, 3, "all three hooks must execute")
	assert.Equal(t, []int{1, 2, 3}, order, "hooks must execute in registration order")
}

func TestShutdownHook_ErrorDoesNotStopSubsequentHooks(t *testing.T) {
	t.Parallel()

	logger := &recordingLogger{}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	shutdownChan := make(chan struct{})

	hookErr := errors.New("hook1 intentional failure")

	var mu sync.Mutex

	var executed []int

	sm := server.NewServerManager(nil, nil, logger).
		WithHTTPServer(app, ":0").
		WithShutdownChannel(shutdownChan).
		WithShutdownHook(func(_ context.Context) error {
			mu.Lock()
			defer mu.Unlock()

			executed = append(executed, 1)

			return hookErr
		}).
		WithShutdownHook(func(_ context.Context) error {
			mu.Lock()
			defer mu.Unlock()

			executed = append(executed, 2)

			return nil
		}).
		WithShutdownHook(func(_ context.Context) error {
			mu.Lock()
			defer mu.Unlock()

			executed = append(executed, 3)

			return nil
		})

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for servers to start")
	}

	close(shutdownChan)

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for shutdown")
	}

	// All three hooks must have run despite hook1 returning an error.
	mu.Lock()
	defer mu.Unlock()

	require.Len(t, executed, 3, "all three hooks must execute even when one fails")
	assert.Equal(t, []int{1, 2, 3}, executed,
		"hooks must execute in order regardless of prior errors")

	// Verify the error from hook1 was logged.
	msgs := logger.getMessages()
	assert.Contains(t, msgs, "shutdown hook failed",
		"failing hook error should be logged")
}
