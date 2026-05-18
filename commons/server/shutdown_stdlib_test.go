//go:build unit

package server_test

import (
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/server"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// reserveFreeAddr returns a "127.0.0.1:PORT" string for a port the kernel
// just handed out, after closing the listener that briefly held it. There is
// a small TOCTOU window between Close and the test re-binding, but for
// localhost unit tests this is reliable enough.
func reserveFreeAddr(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := l.Addr().String()
	require.NoError(t, l.Close())

	return addr
}

func newTestStdlibListener(t *testing.T) net.Listener {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	return listener
}

// waitForHTTPListening polls a TCP address until it accepts connections or
// the timeout expires. Mirrors waitForTCP in the integration suite — kept
// local here so this file stands alone under the unit build tag.
func waitForHTTPListening(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("stdlib HTTP server did not become reachable at %s within %s", addr, timeout)
}

// newTestStdlibServer constructs a stdlib *http.Server bound to addr with the
// given handler. Timeouts are conservative but populated so the server isn't
// vulnerable to slowloris during the test — and to exercise the documented
// requirement that callers populate timeouts before passing the server in.
func newTestStdlibServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       5 * time.Second,
	}
}

// TestStdlibHTTPServer_GracefulShutdownOnSignal covers scenario 1:
// Stdlib server + signal → graceful drain. Parity with the fiber equivalent
// (TestStartWithGracefulShutdownWithError_HTTPServer_Success).
func TestStdlibHTTPServer_GracefulShutdownOnSignal(t *testing.T) {
	listener := newTestStdlibListener(t)
	addr := listener.Addr().String()

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "pong"); err != nil {
			t.Errorf("write ping response: %v", err)
		}
	})

	srv := newTestStdlibServer(addr, mux)
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithStdlibHTTPListener(srv, listener).
		WithShutdownChannel(shutdownChan).
		WithShutdownTimeout(5 * time.Second)

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for ServersStarted signal")
	}

	waitForHTTPListening(t, addr, 5*time.Second)

	// Verify the server is actually serving before we shut it down.
	resp, err := http.Get("http://" + addr + "/ping")
	require.NoError(t, err)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "pong", string(body))

	close(shutdownChan)

	select {
	case err := <-done:
		assert.NoError(t, err, "clean shutdown should return nil (http.ErrServerClosed mapped to nil)")
	case <-time.After(10 * time.Second):
		t.Fatal("Timed out waiting for stdlib HTTP server graceful shutdown")
	}

	// Verify the socket is no longer reachable.
	_, err = net.DialTimeout("tcp", addr, 200*time.Millisecond)
	assert.Error(t, err, "stdlib HTTP server should no longer accept connections after shutdown")
}

// TestStdlibHTTPServer_BootFailurePortInUse covers scenario 2:
// Stdlib server + boot failure (port in use) → boot error surfaces as the
// return value of StartWithGracefulShutdownWithError.
func TestStdlibHTTPServer_BootFailurePortInUse(t *testing.T) {
	// Hold an actual listener open so ListenAndServe fails synchronously
	// with EADDRINUSE.
	occupier, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	t.Cleanup(func() { _ = occupier.Close() })

	srv := newTestStdlibServer(occupier.Addr().String(), http.NewServeMux())

	sm := server.NewServerManager(nil, nil, nil).
		WithStdlibHTTPServer(srv)
	// No WithShutdownChannel: exercise the OS-signal path which exits on a
	// startupErrors write rather than waiting on a signal.

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case err := <-done:
		require.Error(t, err, "boot failure should surface through StartWithGracefulShutdownWithError")
		assert.Contains(t, err.Error(), "HTTP server", "wrapped error should preserve the HTTP server context")
		// Sanity-check that it's NOT http.ErrServerClosed (which would mean
		// the clean-shutdown mapping accidentally swallowed a real boot
		// error).
		assert.False(t, errors.Is(err, http.ErrServerClosed),
			"boot failure must not be confused with the clean-shutdown sentinel")
	case <-time.After(10 * time.Second):
		t.Fatal("Timed out: stdlib HTTP boot error was not propagated")
	}
}

// TestStdlibHTTPServer_InFlightRequestDrains covers scenario 3:
// Stdlib server + in-flight slow request + signal → request completes within
// the drain window. This is the fundamental graceful-shutdown property: no
// request is dropped mid-flight.
func TestStdlibHTTPServer_InFlightRequestDrains(t *testing.T) {
	listener := newTestStdlibListener(t)
	addr := listener.Addr().String()

	const slowEndpointDuration = 500 * time.Millisecond

	var requestCompleted atomic.Bool
	handlerEntered := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(handlerEntered)
		time.Sleep(slowEndpointDuration)
		requestCompleted.Store(true)
		if _, err := io.WriteString(w, "done"); err != nil {
			t.Errorf("write slow response: %v", err)
		}
	})

	srv := newTestStdlibServer(addr, mux)
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithStdlibHTTPListener(srv, listener).
		WithShutdownChannel(shutdownChan).
		WithShutdownTimeout(10 * time.Second)

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for ServersStarted")
	}

	waitForHTTPListening(t, addr, 5*time.Second)

	// Launch the slow request in the background.
	requestResultCh := make(chan *http.Response, 1)
	requestErrCh := make(chan error, 1)

	go func() {
		client := &http.Client{Timeout: 10 * time.Second}

		resp, err := client.Get("http://" + addr + "/slow")
		if err != nil {
			requestErrCh <- err
			return
		}

		requestResultCh <- resp
	}()

	// Wait until the handler has definitely started before triggering shutdown.
	// This avoids racing shutdown initiation against request arrival.
	select {
	case <-handlerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for slow handler to start")
	}
	assert.False(t, requestCompleted.Load(),
		"slow request must still be in-flight when shutdown is triggered")

	close(shutdownChan)

	// The in-flight request must finish successfully.
	select {
	case resp := <-requestResultCh:
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "done", string(body),
			"in-flight request must receive the full response body")
	case err := <-requestErrCh:
		t.Fatalf("in-flight request was dropped during shutdown: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("Timed out waiting for in-flight request to complete")
	}

	assert.True(t, requestCompleted.Load(),
		"slow handler must have run to completion before server exit")

	select {
	case err := <-done:
		assert.NoError(t, err, "server should exit cleanly after draining in-flight requests")
	case <-time.After(10 * time.Second):
		t.Fatal("Timed out waiting for server exit")
	}
}

// TestStdlibHTTPServer_DrainTimeoutSurfacesError covers scenario 4:
// Stdlib server + drain-timeout exhaustion → the Shutdown error is logged
// via the manager's error log path. We assert on the structured log record
// rather than the return value: by design, shutdown errors are best-effort
// cleanup and do not propagate through StartWithGracefulShutdownWithError
// (which only surfaces *startup* errors, identical to the fiber path).
//
// The drain-timeout pressure is generated by a handler that blocks longer
// than shutdownTimeout, so http.Server.Shutdown returns ctx.DeadlineExceeded.
func TestStdlibHTTPServer_DrainTimeoutSurfacesError(t *testing.T) {
	listener := newTestStdlibListener(t)
	addr := listener.Addr().String()

	// Drain timeout is intentionally short.
	const drainTimeout = 200 * time.Millisecond

	// Handler blocks far longer than drainTimeout so Shutdown's ctx
	// exceeds its deadline before the connection drains. The handler
	// uses a long sleep with a context-aware select so a panic in the
	// test runner cannot leak this goroutine indefinitely.
	handlerDone := make(chan struct{})
	handlerEntered := make(chan struct{})
	t.Cleanup(func() { close(handlerDone) })

	mux := http.NewServeMux()
	mux.HandleFunc("/block", func(w http.ResponseWriter, _ *http.Request) {
		close(handlerEntered)

		select {
		case <-time.After(5 * time.Second):
		case <-handlerDone:
		}

		if _, err := io.WriteString(w, "late"); err != nil {
			t.Errorf("write late response: %v", err)
		}
	})

	srv := newTestStdlibServer(addr, mux)

	logger := &recordingLogger{}
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, nil, logger).
		WithStdlibHTTPListener(srv, listener).
		WithShutdownChannel(shutdownChan).
		WithShutdownTimeout(drainTimeout)

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for ServersStarted")
	}

	waitForHTTPListening(t, addr, 5*time.Second)

	// Launch a request that will be in-flight when shutdown begins.
	requestErrCh := make(chan error, 1)

	go func() {
		client := &http.Client{Timeout: 10 * time.Second}

		resp, err := client.Get("http://" + addr + "/block")
		if resp != nil {
			if closeErr := resp.Body.Close(); closeErr != nil {
				requestErrCh <- closeErr

				return
			}
		}

		requestErrCh <- err
	}()

	select {
	case <-handlerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for blocking handler to start")
	}

	close(shutdownChan)

	// The manager should still return without error even though
	// http.Server.Shutdown internally returned ctx.DeadlineExceeded —
	// shutdown errors are logged, not propagated. This matches the fiber
	// path's behavior.
	select {
	case err := <-done:
		assert.NoError(t, err,
			"shutdown drain errors are logged, not returned (parity with fiber path)")
	case <-time.After(10 * time.Second):
		t.Fatal("Timed out waiting for server exit")
	}

	// Drain the request goroutine so it doesn't leak. Shutdown timeout must now
	// trigger a hard Close fallback, so the blocked request should be released.
	select {
	case err := <-requestErrCh:
		assert.Error(t, err, "hard-close fallback should release the in-flight client with an error")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for hard-close fallback to release in-flight request")
	}

	msgs := logger.getMessages()
	// The drain error must have hit the structured log path.
	require.Contains(t, msgs, "error during HTTP server shutdown",
		"http.Server.Shutdown timeout must surface via the error log channel")
}

func TestStdlibHTTPServer_WithStdlibHTTPServer_DefaultsReadHeaderTimeout(t *testing.T) {
	t.Parallel()

	srv := &http.Server{Addr: "127.0.0.1:0", Handler: http.NewServeMux()}

	server.NewServerManager(nil, nil, nil).WithStdlibHTTPServer(srv)

	assert.Equal(t, 5*time.Second, srv.ReadHeaderTimeout)

	explicit := &http.Server{
		Addr:              "127.0.0.1:0",
		Handler:           http.NewServeMux(),
		ReadHeaderTimeout: 123 * time.Millisecond,
	}
	server.NewServerManager(nil, nil, nil).WithStdlibHTTPServer(explicit)
	assert.Equal(t, 123*time.Millisecond, explicit.ReadHeaderTimeout)
}

func TestServerManager_ServersStarted_ZeroValueReturnsNonNilChannel(t *testing.T) {
	t.Parallel()

	sm := &server.ServerManager{}

	ch := sm.ServersStarted()

	assert.NotNil(t, ch, "zero-value ServerManager must not expose a nil ServersStarted channel")
}

// TestStdlibHTTPServer_MutualExclusionWithFiber covers scenario 5:
// WithHTTPServer(...).WithStdlibHTTPServer(...) reports a configuration
// error before launching anything.
func TestStdlibHTTPServer_MutualExclusionWithFiber(t *testing.T) {
	t.Parallel()

	fiberApp := fiber.New(fiber.Config{DisableStartupMessage: true})
	stdlibSrv := &http.Server{
		Addr:              "127.0.0.1:0",
		Handler:           http.NewServeMux(),
		ReadHeaderTimeout: time.Second,
	}

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(fiberApp, ":0").
		WithStdlibHTTPServer(stdlibSrv)

	err := sm.StartWithGracefulShutdownWithError()
	require.Error(t, err)
	assert.True(t, errors.Is(err, server.ErrConflictingHTTPServers),
		"mutual exclusion must report ErrConflictingHTTPServers; got %v", err)

	// ServersStarted should NOT have been closed: validation rejected the
	// configuration before any goroutine launched. We detect this by
	// observing that the channel is still open (a recv would block).
	select {
	case <-sm.ServersStarted():
		t.Fatal("ServersStarted closed despite a configuration error — goroutines must not launch on validation failure")
	default:
		// Expected: validation rejected before launch.
	}

	// Mutual exclusion must hold regardless of which configurator is called
	// first: invert the order and confirm.
	sm2 := server.NewServerManager(nil, nil, nil).
		WithStdlibHTTPServer(stdlibSrv).
		WithHTTPServer(fiberApp, ":0")

	err = sm2.StartWithGracefulShutdownWithError()
	require.Error(t, err)
	assert.True(t, errors.Is(err, server.ErrConflictingHTTPServers),
		"mutual exclusion must hold regardless of configurator order; got %v", err)
}

// TestStdlibHTTPServer_WithGRPCBothDrain covers scenario 6:
// Stdlib HTTP server + gRPC server → both launch, both drain on signal,
// gRPC drains after stdlib HTTP (mirrors the existing fiber+gRPC test
// TestStartWithGracefulShutdownWithError_BothServers_Success). The
// drain-order assertion is enforced via a log-message ordering check
// against the recordingLogger.
func TestStdlibHTTPServer_WithGRPCBothDrain(t *testing.T) {
	httpListener := newTestStdlibListener(t)
	httpAddr := httpListener.Addr().String()
	grpcAddr := reserveFreeAddr(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	stdlibSrv := newTestStdlibServer(httpAddr, mux)
	grpcSrv := grpc.NewServer()

	logger := &recordingLogger{}
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, nil, logger).
		WithStdlibHTTPListener(stdlibSrv, httpListener).
		WithGRPCServer(grpcSrv, grpcAddr).
		WithShutdownChannel(shutdownChan).
		WithShutdownTimeout(5 * time.Second)

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for ServersStarted")
	}

	// Confirm both sockets are bound before triggering shutdown.
	waitForHTTPListening(t, httpAddr, 5*time.Second)
	waitForHTTPListening(t, grpcAddr, 5*time.Second)

	close(shutdownChan)

	select {
	case err := <-done:
		assert.NoError(t, err, "both servers should drain cleanly on signal")
	case <-time.After(15 * time.Second):
		t.Fatal("Timed out waiting for both servers to drain")
	}

	msgs := logger.getMessages()

	// Both shutdown banners must appear, in the order: stdlib HTTP first,
	// then gRPC. This mirrors the fiber+gRPC drain order (server before
	// gRPC) and proves the stdlib branch slotted into the same lifecycle
	// position as the fiber branch.
	httpIdx := indexOf(msgs, "Shutting down HTTP server...")
	grpcIdx := indexOf(msgs, "Shutting down gRPC server...")

	require.GreaterOrEqual(t, httpIdx, 0, "HTTP shutdown banner must appear in logs (messages=%v)", msgs)
	require.GreaterOrEqual(t, grpcIdx, 0, "gRPC shutdown banner must appear in logs (messages=%v)", msgs)
	assert.Less(t, httpIdx, grpcIdx,
		"stdlib HTTP server must drain BEFORE the gRPC server (parity with fiber+gRPC ordering)")
}

// indexOf returns the position of needle in haystack, or -1 if absent.
// Defined locally to avoid pulling in slices/strings helpers and to keep
// the test file self-contained.
func indexOf(haystack []string, needle string) int {
	for i, s := range haystack {
		if s == needle {
			return i
		}
	}

	return -1
}

// --- Lightweight smoke tests for the configurators themselves -------------

// TestWithStdlibHTTPServer_NilReceiver asserts that calling
// WithStdlibHTTPServer on a nil *ServerManager does not panic and returns
// nil — parity with the existing nil-receiver guards on WithHTTPServer and
// WithGRPCServer.
func TestWithStdlibHTTPServer_NilReceiver(t *testing.T) {
	t.Parallel()

	var sm *server.ServerManager

	assert.NotPanics(t, func() {
		result := sm.WithStdlibHTTPServer(&http.Server{Addr: ":0"})
		assert.Nil(t, result, "nil receiver must return nil")
	})
}

// TestWithStdlibHTTPServer_Chaining asserts the fluent-builder shape: the
// method returns the same *ServerManager so it composes with the other
// With* configurators.
func TestWithStdlibHTTPServer_Chaining(t *testing.T) {
	t.Parallel()

	srv := &http.Server{Addr: ":0", ReadHeaderTimeout: time.Second}
	sm := server.NewServerManager(nil, nil, nil)
	result := sm.WithStdlibHTTPServer(srv)
	assert.Same(t, sm, result, "WithStdlibHTTPServer must return the same manager for chaining")
}

// TestStdlibHTTPServer_NoServersConfigured confirms that the existing
// "no servers" check still fires when neither fiber, stdlib, nor gRPC is
// configured — the new mutual-exclusion check must not displace it.
func TestStdlibHTTPServer_NoServersConfigured(t *testing.T) {
	t.Parallel()

	sm := server.NewServerManager(nil, nil, nil)
	err := sm.StartWithGracefulShutdownWithError()
	require.Error(t, err)
	assert.True(t, errors.Is(err, server.ErrNoServersConfigured),
		"empty manager must still return ErrNoServersConfigured; got %v", err)
}
