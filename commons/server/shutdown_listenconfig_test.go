//go:build unit

package server_test

import (
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v6/commons/server"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	fiberStartupMessageMarker = "Server started on"
	fiberBannerMarker         = "Fiber"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	require.NoError(t, err)

	original := os.Stdout
	os.Stdout = w

	collected := make(chan string, 1)

	go func() {
		var sb strings.Builder

		_, _ = io.Copy(&sb, r)

		collected <- sb.String()
	}()

	func() {
		defer func() {
			os.Stdout = original

			_ = w.Close()
		}()

		fn()
	}()

	out := <-collected

	_ = r.Close()

	return out
}

func newProbeApp(t *testing.T) *fiber.App {
	t.Helper()

	app := fiber.New()
	app.Get("/probe", func(c fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})

	return app
}

func waitUntilServing(t *testing.T, addr string) {
	t.Helper()

	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + addr + "/probe")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				return
			}
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("fiber app never served a request on %s", addr)
}

func runFiberManagerUntilServed(t *testing.T, sm *server.ServerManager, shutdownChan chan struct{}, addr string) {
	t.Helper()

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm.ServersStarted():
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for server goroutines to launch")
	}

	waitUntilServing(t, addr)

	close(shutdownChan)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for graceful shutdown to complete")
	}
}

func TestWithHTTPServerConfig_DisableStartupMessageSuppressesBanner(t *testing.T) {
	app := newProbeApp(t)
	addr := reserveFreeAddr(t)
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServerConfig(app, addr, fiber.ListenConfig{DisableStartupMessage: true}).
		WithShutdownChannel(shutdownChan)

	out := captureStdout(t, func() {
		runFiberManagerUntilServed(t, sm, shutdownChan, addr)
	})

	assert.NotContains(t, out, fiberStartupMessageMarker,
		"DisableStartupMessage must suppress fiber's unstructured startup line; captured stdout: %q", out)
	assert.NotContains(t, out, fiberBannerMarker,
		"DisableStartupMessage must suppress fiber's ASCII banner; captured stdout: %q", out)
	assert.Empty(t, out, "no stdout output is expected when the startup message is disabled")
}

func TestWithHTTPServer_StartupMessageStillPrintedByDefault(t *testing.T) {
	app := newProbeApp(t)
	addr := reserveFreeAddr(t)
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(app, addr).
		WithShutdownChannel(shutdownChan)

	out := captureStdout(t, func() {
		runFiberManagerUntilServed(t, sm, shutdownChan, addr)
	})

	assert.Contains(t, out, fiberStartupMessageMarker,
		"WithHTTPServer must keep fiber's default startup message; suppressing it by default would silently change behavior for every existing consumer")
}

func TestWithHTTPServerConfig_ListenConfigReachesListen(t *testing.T) {
	app := newProbeApp(t)
	addr := reserveFreeAddr(t)
	shutdownChan := make(chan struct{})

	var (
		mu       sync.Mutex
		observed string
	)

	cfg := fiber.ListenConfig{
		DisableStartupMessage: true,
		ListenerAddrFunc: func(a net.Addr) {
			mu.Lock()
			defer mu.Unlock()

			observed = a.String()
		},
	}

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServerConfig(app, addr, cfg).
		WithShutdownChannel(shutdownChan)

	runFiberManagerUntilServed(t, sm, shutdownChan, addr)

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, addr, observed,
		"ListenerAddrFunc from the supplied fiber.ListenConfig must be invoked with the bound address")
}

func TestWithHTTPServerConfig_MutualExclusionWithStdlibHTTPServer(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	stdlibSrv := &http.Server{
		Addr:              "127.0.0.1:0",
		Handler:           http.NewServeMux(),
		ReadHeaderTimeout: time.Second,
	}

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServerConfig(app, ":0", fiber.ListenConfig{DisableStartupMessage: true}).
		WithStdlibHTTPServer(stdlibSrv)

	err := sm.StartWithGracefulShutdownWithError()
	require.Error(t, err)
	require.ErrorIs(t, err, server.ErrConflictingHTTPServers)

	select {
	case <-sm.ServersStarted():
		t.Fatal("ServersStarted closed despite a configuration error")
	default:
	}

	sm2 := server.NewServerManager(nil, nil, nil).
		WithStdlibHTTPServer(stdlibSrv).
		WithHTTPServerConfig(app, ":0", fiber.ListenConfig{DisableStartupMessage: true})

	err = sm2.StartWithGracefulShutdownWithError()
	require.Error(t, err)
	require.ErrorIs(t, err, server.ErrConflictingHTTPServers)
}

func TestWithHTTPServerConfig_MutualExclusionWithStdlibHTTPListener(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = ln.Close()
	})

	app := fiber.New()
	stdlibSrv := &http.Server{
		Handler:           http.NewServeMux(),
		ReadHeaderTimeout: time.Second,
	}

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServerConfig(app, ":0", fiber.ListenConfig{DisableStartupMessage: true}).
		WithStdlibHTTPListener(stdlibSrv, ln)

	err = sm.StartWithGracefulShutdownWithError()
	require.Error(t, err)
	require.ErrorIs(t, err, server.ErrConflictingHTTPServers)
}

func TestWithHTTPServerConfig_NilReceiver(t *testing.T) {
	t.Parallel()

	var sm *server.ServerManager

	assert.Nil(t, sm.WithHTTPServerConfig(fiber.New(), ":0", fiber.ListenConfig{}))
}

func TestWithHTTPServerConfig_LastConfiguratorWins(t *testing.T) {
	t.Parallel()

	app := newProbeApp(t)
	addr := reserveFreeAddr(t)
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(app, ":0").
		WithHTTPServerConfig(app, addr, fiber.ListenConfig{DisableStartupMessage: true}).
		WithShutdownChannel(shutdownChan)

	runFiberManagerUntilServed(t, sm, shutdownChan, addr)
}
