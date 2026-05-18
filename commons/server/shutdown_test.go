//go:build unit

package server_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/license"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/server"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// recordingLogger is a Logger that records messages and can return a Sync error.
type recordingLogger struct {
	mu       sync.Mutex
	messages []string
	syncErr  error
}

func (l *recordingLogger) Log(_ context.Context, _ log.Level, msg string, _ ...log.Field) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.messages = append(l.messages, msg)
}

func (l *recordingLogger) With(_ ...log.Field) log.Logger { return l }
func (l *recordingLogger) WithGroup(_ string) log.Logger  { return l }
func (l *recordingLogger) Enabled(_ log.Level) bool       { return true }
func (l *recordingLogger) Sync(_ context.Context) error   { return l.syncErr }
func (l *recordingLogger) getMessages() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	cp := make([]string, len(l.messages))
	copy(cp, l.messages)

	return cp
}

func TestNewServerManager(t *testing.T) {
	sm := server.NewServerManager(nil, nil, nil)
	assert.NotNil(t, sm, "NewServerManager should return a non-nil instance")
}

func TestServerManagerWithHTTPOnly(t *testing.T) {
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})
	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(app, ":8080")
	assert.NotNil(t, sm, "ServerManager with HTTP server should return a non-nil instance")
}

func TestServerManagerWithGRPCOnly(t *testing.T) {
	grpcServer := grpc.NewServer()
	sm := server.NewServerManager(nil, nil, nil).
		WithGRPCServer(grpcServer, ":50051")
	assert.NotNil(t, sm, "ServerManager with gRPC server should return a non-nil instance")
}

func TestServerManagerWithBothServers(t *testing.T) {
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})
	grpcServer := grpc.NewServer()
	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(app, ":8080").
		WithGRPCServer(grpcServer, ":50051")
	assert.NotNil(t, sm, "ServerManager with both servers should return a non-nil instance")
}

func TestServerManagerChaining(t *testing.T) {
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})
	grpcServer := grpc.NewServer()

	// Test method chaining
	sm1 := server.NewServerManager(nil, nil, nil).WithHTTPServer(app, ":8080")
	sm2 := sm1.WithGRPCServer(grpcServer, ":50051")

	assert.Equal(t, sm1, sm2, "Method chaining should return the same instance")
}

func TestStartWithGracefulShutdownWithError_NoServers(t *testing.T) {
	sm := server.NewServerManager(nil, nil, nil)

	err := sm.StartWithGracefulShutdownWithError()

	assert.Error(t, err)
	assert.True(t, errors.Is(err, server.ErrNoServersConfigured))
}

func TestErrNoServersConfigured(t *testing.T) {
	assert.NotNil(t, server.ErrNoServersConfigured)
	assert.Contains(t, server.ErrNoServersConfigured.Error(), "no servers configured")
}

func TestStartWithGracefulShutdownWithError_HTTPServer_Success(t *testing.T) {
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(app, ":0").
		WithShutdownChannel(shutdownChan)

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("Test timed out waiting for servers to start")
	}

	close(shutdownChan)

	select {
	case err := <-done:
		assert.NoError(t, err, "StartWithGracefulShutdownWithError should complete without error")
	case <-time.After(5 * time.Second):
		t.Fatal("Test timed out waiting for StartWithGracefulShutdownWithError to complete")
	}
}

func TestStartWithGracefulShutdownWithError_GRPCServer_Success(t *testing.T) {
	grpcServer := grpc.NewServer()
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithGRPCServer(grpcServer, ":0").
		WithShutdownChannel(shutdownChan)

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("Test timed out waiting for servers to start")
	}

	close(shutdownChan)

	select {
	case err := <-done:
		assert.NoError(t, err, "StartWithGracefulShutdownWithError should complete without error")
	case <-time.After(5 * time.Second):
		t.Fatal("Test timed out waiting for StartWithGracefulShutdownWithError to complete")
	}
}

func TestStartWithGracefulShutdownWithError_BothServers_Success(t *testing.T) {
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})
	grpcServer := grpc.NewServer()
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(app, ":0").
		WithGRPCServer(grpcServer, ":0").
		WithShutdownChannel(shutdownChan)

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("Test timed out waiting for servers to start")
	}

	close(shutdownChan)

	select {
	case err := <-done:
		assert.NoError(t, err, "StartWithGracefulShutdownWithError should complete without error")
	case <-time.After(5 * time.Second):
		t.Fatal("Test timed out waiting for StartWithGracefulShutdownWithError to complete")
	}
}

func TestWithShutdownChannel(t *testing.T) {
	shutdownChan := make(chan struct{})
	sm := server.NewServerManager(nil, nil, nil).
		WithShutdownChannel(shutdownChan)
	assert.NotNil(t, sm, "WithShutdownChannel should return a non-nil instance")
}

func TestWithShutdownTimeout(t *testing.T) {
	sm := server.NewServerManager(nil, nil, nil).
		WithShutdownTimeout(10 * time.Second)
	assert.NotNil(t, sm, "WithShutdownTimeout should return a non-nil instance")
}

func TestStartWithGracefulShutdownWithError_HTTPStartupError(t *testing.T) {
	// Bind a port so the HTTP server will fail to listen
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	defer ln.Close()

	occupiedAddr := ln.Addr().String()

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(app, occupiedAddr)

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	// The startup error should propagate through the return value.
	select {
	case err := <-done:
		require.Error(t, err, "StartWithGracefulShutdownWithError should propagate startup error")
		assert.Contains(t, err.Error(), "HTTP server")
	case <-time.After(10 * time.Second):
		t.Fatal("Test timed out: startup error was not propagated")
	}
}

func TestExecuteShutdown_Idempotent(t *testing.T) {
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(app, ":0").
		WithShutdownChannel(shutdownChan)

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("Test timed out waiting for servers to start")
	}

	// Trigger shutdown
	close(shutdownChan)

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Test timed out waiting for shutdown")
	}

	// Second shutdown call should be safe (no-op due to sync.Once)
	assert.NotPanics(t, func() {
		// Call StartWithGracefulShutdownWithError again - it will call executeShutdown
		// but sync.Once ensures the shutdown body runs only once
		// We can't call it directly since executeShutdown is unexported,
		// but we can verify the manager is stable after shutdown
		_ = sm.StartWithGracefulShutdownWithError()
	}, "Second invocation after shutdown should not panic")
}

func TestStartWithGracefulShutdownWithError_GRPCShutdownTimeout(t *testing.T) {
	grpcServer := grpc.NewServer()
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithGRPCServer(grpcServer, ":0").
		WithShutdownChannel(shutdownChan).
		WithShutdownTimeout(1 * time.Second)

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("Test timed out waiting for servers to start")
	}

	close(shutdownChan)

	select {
	case err := <-done:
		assert.NoError(t, err, "Shutdown with timeout should complete without error")
	case <-time.After(10 * time.Second):
		t.Fatal("Test timed out: gRPC shutdown timeout did not work")
	}
}

func TestServerManager_NilLoggerSafe(t *testing.T) {
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})
	shutdownChan := make(chan struct{})

	// Explicitly pass nil logger
	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(app, ":0").
		WithShutdownChannel(shutdownChan)

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("Test timed out waiting for servers to start")
	}

	close(shutdownChan)

	select {
	case err := <-done:
		assert.NoError(t, err, "Nil logger should not cause panics during lifecycle")
	case <-time.After(5 * time.Second):
		t.Fatal("Test timed out")
	}
}

func TestStartWithGracefulShutdownWithError_ManualZeroValueManager_NoPanic(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	shutdownChan := make(chan struct{})
	close(shutdownChan)

	// Use a manually instantiated zero-value manager to verify nil-safe defaults.
	sm := (&server.ServerManager{}).
		WithHTTPServer(app, ":0").
		WithShutdownChannel(shutdownChan)

	assert.NotPanics(t, func() {
		err := sm.StartWithGracefulShutdownWithError()
		assert.NoError(t, err)
	})
}

func TestExecuteShutdown_WithTelemetry(t *testing.T) {
	logger := &recordingLogger{}

	tel, err := opentelemetry.NewTelemetry(opentelemetry.TelemetryConfig{
		EnableTelemetry: false,
		Logger:          logger,
		LibraryName:     "test",
	})
	require.NoError(t, err)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, tel, logger).
		WithHTTPServer(app, ":0").
		WithShutdownChannel(shutdownChan)

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

	msgs := logger.getMessages()
	assert.Contains(t, msgs, "Shutting down telemetry...")
}

func TestExecuteShutdown_WithLicenseClient(t *testing.T) {
	logger := &recordingLogger{}
	lc := license.New()

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(lc, nil, logger).
		WithHTTPServer(app, ":0").
		WithShutdownChannel(shutdownChan)

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

	msgs := logger.getMessages()
	assert.Contains(t, msgs, "Skipping license termination handler during graceful shutdown")
}

func TestExecuteShutdown_LoggerSyncError(t *testing.T) {
	logger := &recordingLogger{syncErr: errors.New("sync failed")}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, nil, logger).
		WithHTTPServer(app, ":0").
		WithShutdownChannel(shutdownChan)

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

	msgs := logger.getMessages()
	assert.Contains(t, msgs, "failed to sync logger")
}

func TestExecuteShutdown_WithAllComponents(t *testing.T) {
	logger := &recordingLogger{}

	tel, err := opentelemetry.NewTelemetry(opentelemetry.TelemetryConfig{
		EnableTelemetry: false,
		Logger:          logger,
		LibraryName:     "test",
	})
	require.NoError(t, err)

	lc := license.New()

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	grpcServer := grpc.NewServer()
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(lc, tel, logger).
		WithHTTPServer(app, ":0").
		WithGRPCServer(grpcServer, ":0").
		WithShutdownChannel(shutdownChan)

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
	case <-time.After(10 * time.Second):
		t.Fatal("Timed out waiting for shutdown")
	}

	msgs := logger.getMessages()
	assert.Contains(t, msgs, "Shutting down telemetry...")
	assert.Contains(t, msgs, "Skipping license termination handler during graceful shutdown")
	assert.Contains(t, msgs, "Graceful shutdown completed")
}

func TestStartWithGracefulShutdownWithError_GRPCStartupError(t *testing.T) {
	// Bind a port so the gRPC server will fail to listen
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	occupiedAddr := ln.Addr().String()

	logger := &recordingLogger{}
	grpcServer := grpc.NewServer()

	sm := server.NewServerManager(nil, nil, logger).
		WithGRPCServer(grpcServer, occupiedAddr)

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case err := <-done:
		require.Error(t, err, "StartWithGracefulShutdownWithError should propagate gRPC startup error")
		assert.Contains(t, err.Error(), "gRPC listen")
	case <-time.After(10 * time.Second):
		t.Fatal("Timed out: gRPC startup error was not propagated")
	}
}

func TestExecuteShutdown_HTTPShutdownError(t *testing.T) {
	logger := &recordingLogger{}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, nil, logger).
		WithHTTPServer(app, ":0").
		WithShutdownChannel(shutdownChan)

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for servers to start")
	}

	// Shut down HTTP server manually before triggering shutdown to cause error
	_ = app.Shutdown()

	close(shutdownChan)

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for shutdown")
	}
}

func TestStartWithGracefulShutdownWithError_WithRealLogger(t *testing.T) {
	logger := &recordingLogger{}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, nil, logger).
		WithHTTPServer(app, ":0").
		WithShutdownChannel(shutdownChan)

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

	msgs := logger.getMessages()
	assert.Contains(t, msgs, "Gracefully shutting down all servers...")
	assert.Contains(t, msgs, "Syncing logger...")
	assert.Contains(t, msgs, "Graceful shutdown completed")
}

func TestStartWithGracefulShutdownWithError_StartupErrorViaOSSignalPath(t *testing.T) {
	// Exercise the OS-signal path in handleShutdown with a startup error
	// (no shutdown channel, so it hits the else branch with signal.Notify).
	logger := &recordingLogger{}

	// Use an occupied port so the HTTP server fails immediately.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	app := fiber.New(fiber.Config{DisableStartupMessage: true})

	sm := server.NewServerManager(nil, nil, logger).
		WithHTTPServer(app, ln.Addr().String())
	// No WithShutdownChannel — uses the OS signal path.

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case err := <-done:
		require.Error(t, err, "StartWithGracefulShutdownWithError should propagate startup error via OS signal path")
		assert.Contains(t, err.Error(), "HTTP server")
	case <-time.After(10 * time.Second):
		t.Fatal("Timed out: startup error via OS signal path was not propagated")
	}
}

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
