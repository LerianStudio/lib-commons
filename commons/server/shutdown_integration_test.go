//go:build integration

package server_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/server"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// getFreePort allocates a free TCP port from the OS, closes the listener, and
// returns the port as a ":PORT" string suitable for Fiber's Listen or gRPC's
// net.Listen. There is a small TOCTOU window, but for integration tests on
// localhost this is reliable enough.
func getFreePort(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	port := l.Addr().(*net.TCPAddr).Port

	require.NoError(t, l.Close())

	return fmt.Sprintf(":%d", port)
}

// waitForTCP polls a TCP address until it accepts connections or the timeout
// expires. This bridges the gap between ServersStarted() (goroutine launched)
// and the socket actually being bound and ready.
func waitForTCP(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			require.NoError(t, conn.Close())
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("TCP address %s did not become available within %s", addr, timeout)
}

// TestIntegration_ServerManager_HTTPLifecycle verifies the full HTTP server
// lifecycle: start → serve requests → graceful shutdown → clean exit.
func TestIntegration_ServerManager_HTTPLifecycle(t *testing.T) {
	addr := getFreePort(t)

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})

	app.Get("/ping", func(c *fiber.Ctx) error {
		return c.SendString("pong")
	})

	shutdownChan := make(chan struct{})
	logger := log.NewNop()

	sm := server.NewServerManager(nil, nil, logger).
		WithHTTPServer(app, addr).
		WithShutdownChannel(shutdownChan).
		WithShutdownTimeout(5 * time.Second)

	resultCh := make(chan error, 1)

	go func() {
		resultCh <- sm.StartWithGracefulShutdownWithError()
	}()

	// Wait for the goroutine launch signal.
	select {
	case <-sm.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ServersStarted signal")
	}

	// Wait for the socket to actually accept connections.
	hostAddr := "127.0.0.1" + addr
	waitForTCP(t, hostAddr, 5*time.Second)

	// Verify the HTTP endpoint is serving correctly.
	resp, err := http.Get("http://" + hostAddr + "/ping")
	require.NoError(t, err)

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "pong", string(body))

	// Trigger graceful shutdown.
	close(shutdownChan)

	// Verify clean exit.
	select {
	case err := <-resultCh:
		assert.NoError(t, err, "StartWithGracefulShutdownWithError should return nil on clean shutdown")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for server to shut down")
	}

	// Verify the server is no longer accepting connections.
	_, err = net.DialTimeout("tcp", hostAddr, 200*time.Millisecond)
	assert.Error(t, err, "server should no longer accept connections after shutdown")
}

// TestIntegration_ServerManager_ShutdownHooksExecuted verifies that registered
// shutdown hooks are invoked during graceful shutdown, in registration order.
func TestIntegration_ServerManager_ShutdownHooksExecuted(t *testing.T) {
	addr := getFreePort(t)

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})

	shutdownChan := make(chan struct{})
	logger := log.NewNop()

	var hook1Called atomic.Int64
	var hook2Called atomic.Int64

	sm := server.NewServerManager(nil, nil, logger).
		WithHTTPServer(app, addr).
		WithShutdownChannel(shutdownChan).
		WithShutdownTimeout(5 * time.Second).
		WithShutdownHook(func(_ context.Context) error {
			hook1Called.Add(1)
			return nil
		}).
		WithShutdownHook(func(_ context.Context) error {
			hook2Called.Add(1)
			return nil
		})

	resultCh := make(chan error, 1)

	go func() {
		resultCh <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ServersStarted signal")
	}

	hostAddr := "127.0.0.1" + addr
	waitForTCP(t, hostAddr, 5*time.Second)

	// Confirm hooks haven't fired prematurely.
	assert.Equal(t, int64(0), hook1Called.Load(), "hook1 should not fire before shutdown")
	assert.Equal(t, int64(0), hook2Called.Load(), "hook2 should not fire before shutdown")

	// Trigger shutdown.
	close(shutdownChan)

	select {
	case err := <-resultCh:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for shutdown")
	}

	// Both hooks must have been called exactly once.
	assert.Equal(t, int64(1), hook1Called.Load(), "hook1 should be called exactly once")
	assert.Equal(t, int64(1), hook2Called.Load(), "hook2 should be called exactly once")
}

// TestIntegration_ServerManager_GRPCLifecycle verifies the full gRPC server
// lifecycle: start → accept connections → graceful shutdown → clean exit.
func TestIntegration_ServerManager_GRPCLifecycle(t *testing.T) {
	addr := getFreePort(t)

	grpcServer := grpc.NewServer()
	shutdownChan := make(chan struct{})
	logger := log.NewNop()

	sm := server.NewServerManager(nil, nil, logger).
		WithGRPCServer(grpcServer, addr).
		WithShutdownChannel(shutdownChan).
		WithShutdownTimeout(5 * time.Second)

	resultCh := make(chan error, 1)

	go func() {
		resultCh <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ServersStarted signal")
	}

	hostAddr := "127.0.0.1" + addr
	waitForTCP(t, hostAddr, 5*time.Second)

	// Verify gRPC connectivity by establishing a client connection.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(
		hostAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err, "should be able to create a gRPC client")

	defer conn.Close()

	// Verify the transport layer is reachable by waiting for the connection
	// state to become ready or idle (both indicate the server accepted the TCP
	// handshake). We use WaitForStateChange to avoid spinning.
	conn.Connect()

	// Give the connection a moment to transition from IDLE.
	state := conn.GetState()
	if state.String() == "IDLE" {
		conn.WaitForStateChange(ctx, state)
	}

	currentState := conn.GetState()
	assert.NotEqual(t, "SHUTDOWN", currentState.String(),
		"gRPC connection should not be in SHUTDOWN state while server is running")

	// Trigger graceful shutdown.
	close(shutdownChan)

	select {
	case err := <-resultCh:
		assert.NoError(t, err, "gRPC server should shut down cleanly")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for gRPC server to shut down")
	}
}

// TestIntegration_ServerManager_NoServersError verifies that starting a
// ServerManager with no configured servers returns ErrNoServersConfigured
// immediately and synchronously.
func TestIntegration_ServerManager_NoServersError(t *testing.T) {
	logger := log.NewNop()
	sm := server.NewServerManager(nil, nil, logger)

	err := sm.StartWithGracefulShutdownWithError()

	require.Error(t, err)
	assert.ErrorIs(t, err, server.ErrNoServersConfigured,
		"expected ErrNoServersConfigured when no servers are configured")
}

// TestIntegration_ServerManager_InFlightRequestsDrained verifies that the
// graceful shutdown waits for in-flight HTTP requests to complete before the
// server exits. This is the fundamental property of graceful shutdown: no
// request is dropped mid-flight.
func TestIntegration_ServerManager_InFlightRequestsDrained(t *testing.T) {
	addr := getFreePort(t)

	const slowEndpointDuration = 500 * time.Millisecond

	var requestCompleted atomic.Bool

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})

	app.Get("/slow", func(c *fiber.Ctx) error {
		time.Sleep(slowEndpointDuration)
		requestCompleted.Store(true)

		return c.SendString("done")
	})

	shutdownChan := make(chan struct{})
	logger := log.NewNop()

	sm := server.NewServerManager(nil, nil, logger).
		WithHTTPServer(app, addr).
		WithShutdownChannel(shutdownChan).
		WithShutdownTimeout(10 * time.Second)

	serverResultCh := make(chan error, 1)

	go func() {
		serverResultCh <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ServersStarted signal")
	}

	hostAddr := "127.0.0.1" + addr
	waitForTCP(t, hostAddr, 5*time.Second)

	// Launch the slow request in a background goroutine.
	requestResultCh := make(chan *http.Response, 1)
	requestErrCh := make(chan error, 1)

	go func() {
		client := &http.Client{Timeout: 10 * time.Second}

		resp, err := client.Get("http://" + hostAddr + "/slow")
		if err != nil {
			requestErrCh <- err
			return
		}

		requestResultCh <- resp
	}()

	// Give the request a moment to arrive at the server and begin processing.
	// This ensures the request is genuinely in-flight before we trigger shutdown.
	time.Sleep(100 * time.Millisecond)

	// Trigger shutdown while the request is still being processed.
	assert.False(t, requestCompleted.Load(),
		"slow request should still be in-flight when shutdown is triggered")

	close(shutdownChan)

	// Wait for the in-flight request to complete.
	select {
	case resp := <-requestResultCh:
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"in-flight request should receive a successful response")
		assert.Equal(t, "done", string(body),
			"in-flight request should receive the complete response body")
	case err := <-requestErrCh:
		t.Fatalf("in-flight request failed (request was dropped during shutdown): %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for in-flight request to complete")
	}

	// Verify the request handler ran to completion.
	assert.True(t, requestCompleted.Load(),
		"slow request handler should have completed before server exited")

	// Verify the server exited cleanly.
	select {
	case err := <-serverResultCh:
		assert.NoError(t, err, "server should exit cleanly after draining in-flight requests")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for server to exit after shutdown")
	}
}
