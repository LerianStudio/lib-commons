//go:build unit

package server_test

import (
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/server"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// Tests here that bind real ports do not call t.Parallel: reserveFreeAddr
// closes its probe listener before the server re-binds the port, and parallel
// tests race for that window.

// echoApp returns a fiber app whose single route answers with its own name, so
// a test can tell which of two listening servers replied.
func echoApp(name string) *fiber.App {
	app := fiber.New()

	app.Get("/probe", func(c fiber.Ctx) error {
		return c.SendString(name)
	})

	return app
}

// fetchProbe waits for addr to accept connections and returns the body of
// GET /probe.
func fetchProbe(t *testing.T, addr string) string {
	t.Helper()

	waitForHTTPListening(t, addr, 5*time.Second)

	resp, err := http.Get("http://" + addr + "/probe")
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return string(body)
}

// runManager starts sm in the background and returns the channel carrying its
// exit error, after the server goroutines have been launched.
func runManager(t *testing.T, sm *server.ServerManager) <-chan error {
	t.Helper()

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case <-sm.ServersStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("server goroutines were not launched within 5s")
	}

	return done
}

// requireCleanExit closes the shutdown channel and asserts the manager returned
// without error.
func requireCleanExit(t *testing.T, shutdown chan struct{}, done <-chan error) {
	t.Helper()

	close(shutdown)

	select {
	case err := <-done:
		require.NoError(t, err, "graceful shutdown must not report an error")
	case <-time.After(10 * time.Second):
		t.Fatal("manager did not finish its graceful shutdown within 10s")
	}
}

// drainOrder records the order in which fiber apps completed their shutdown.
// fiber may run the post-shutdown hook more than once when the drain is
// re-issued, so each name is kept only on its first sighting.
type drainOrder struct {
	mu    sync.Mutex
	names []string
}

func (d *drainOrder) record(name string) fiber.OnPostShutdownHandler {
	return func(error) error {
		d.mu.Lock()
		defer d.mu.Unlock()

		if !slices.Contains(d.names, name) {
			d.names = append(d.names, name)
		}

		return nil
	}
}

func (d *drainOrder) seen() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return slices.Clone(d.names)
}

func TestWithAdminHTTPServer_NilReceiver(t *testing.T) {
	t.Parallel()

	var sm *server.ServerManager

	assert.Nil(t, sm.WithAdminHTTPServer(fiber.New(), ":0"))
}

// TestAdminHTTPServerAloneServesAndDrains pins that the admin port is a server
// in its own right: a manager configured with nothing else starts, answers and
// shuts down cleanly.
func TestAdminHTTPServerAloneServesAndDrains(t *testing.T) {
	addr := reserveFreeAddr(t)
	shutdown := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithAdminHTTPServer(echoApp("admin"), addr).
		WithShutdownChannel(shutdown).
		WithShutdownTimeout(5 * time.Second)

	done := runManager(t, sm)

	assert.Equal(t, "admin", fetchProbe(t, addr))

	requireCleanExit(t, shutdown, done)
}

// TestAdminHTTPServerServesBesideTheMainHTTPServer pins that both ports answer
// at the same time, each from its own app.
func TestAdminHTTPServerServesBesideTheMainHTTPServer(t *testing.T) {
	mainAddr := reserveFreeAddr(t)
	adminAddr := reserveFreeAddr(t)
	shutdown := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(echoApp("api"), mainAddr).
		WithAdminHTTPServer(echoApp("admin"), adminAddr).
		WithShutdownChannel(shutdown).
		WithShutdownTimeout(5 * time.Second)

	done := runManager(t, sm)

	assert.Equal(t, "api", fetchProbe(t, mainAddr))
	assert.Equal(t, "admin", fetchProbe(t, adminAddr))

	requireCleanExit(t, shutdown, done)
}

// TestAdminHTTPServerAddressConflict pins that the admin port sharing a socket
// with the main HTTP server is a configuration error caught before any
// goroutine is launched, for every main-server variant and whichever way the
// two addresses are spelled. The wildcard rows are the realistic ones: a
// pre-bound listener always reports its address resolved as "127.0.0.1:PORT",
// while a service writes ":PORT" in its configuration, and a wildcard bind
// takes the port on every interface.
func TestAdminHTTPServerAddressConflict(t *testing.T) {
	variants := map[string]func(t *testing.T, spell func(string) string) *server.ServerManager{
		"fiber main server": func(t *testing.T, spell func(string) string) *server.ServerManager {
			t.Helper()

			addr := reserveFreeAddr(t)

			return server.NewServerManager(nil, nil, nil).
				WithHTTPServer(fiber.New(), addr).
				WithAdminHTTPServer(fiber.New(), spell(addr))
		},
		"stdlib main server": func(t *testing.T, spell func(string) string) *server.ServerManager {
			t.Helper()

			addr := reserveFreeAddr(t)

			return server.NewServerManager(nil, nil, nil).
				WithStdlibHTTPServer(newTestStdlibServer(addr, http.NewServeMux())).
				WithAdminHTTPServer(fiber.New(), spell(addr))
		},
		"stdlib main listener": func(t *testing.T, spell func(string) string) *server.ServerManager {
			t.Helper()

			listener := newTestStdlibListener(t)
			bound := listener.Addr().String()

			return server.NewServerManager(nil, nil, nil).
				WithStdlibHTTPListener(newTestStdlibServer(bound, http.NewServeMux()), listener).
				WithAdminHTTPServer(fiber.New(), spell(bound))
		},
	}

	spellings := map[string]func(string) string{
		"same string":   func(addr string) string { return addr },
		"wildcard host": func(addr string) string { return addr[strings.LastIndex(addr, ":"):] },
	}

	for name, build := range variants {
		for spelling, spell := range spellings {
			t.Run(name+", "+spelling, func(t *testing.T) {
				sm := build(t, spell)

				require.ErrorIs(t, sm.StartWithGracefulShutdownWithError(), server.ErrAdminAddressConflict)

				select {
				case <-sm.ServersStarted():
					t.Fatal("no server goroutine may be launched when the admin address conflicts")
				default:
				}
			})
		}
	}
}

// TestAdminHTTPServerDrainsAfterTheMainHTTPServer pins the shutdown order: the
// admin port is the last to close, so /readyz keeps answering while the API
// drains its in-flight requests.
func TestAdminHTTPServerDrainsAfterTheMainHTTPServer(t *testing.T) {
	order := &drainOrder{}

	mainApp := echoApp("api")
	mainApp.Hooks().OnPostShutdown(order.record("api"))

	adminApp := echoApp("admin")
	adminApp.Hooks().OnPostShutdown(order.record("admin"))

	mainAddr := reserveFreeAddr(t)
	adminAddr := reserveFreeAddr(t)
	shutdown := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(mainApp, mainAddr).
		WithAdminHTTPServer(adminApp, adminAddr).
		WithShutdownChannel(shutdown).
		WithShutdownTimeout(5 * time.Second)

	done := runManager(t, sm)

	waitForHTTPListening(t, mainAddr, 5*time.Second)
	waitForHTTPListening(t, adminAddr, 5*time.Second)

	requireCleanExit(t, shutdown, done)

	assert.Equal(t, []string{"api", "admin"}, order.seen(),
		"the admin server must be the last one to finish draining")
}

// TestAdminHTTPServerAnswersWhileTheMainHTTPServerDrains pins that the admin
// port keeps serving, not merely that it closes last: while an API request is
// still in flight and the API listener is already closed, the admin port
// answers.
func TestAdminHTTPServerAnswersWhileTheMainHTTPServerDrains(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})

	mainApp := fiber.New()
	mainApp.Get("/slow", func(c fiber.Ctx) error {
		close(entered)
		<-release

		return c.SendString("api")
	})

	mainAddr := reserveFreeAddr(t)
	adminAddr := reserveFreeAddr(t)
	shutdown := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(mainApp, mainAddr).
		WithAdminHTTPServer(echoApp("admin"), adminAddr).
		WithShutdownChannel(shutdown).
		WithShutdownTimeout(5 * time.Second)

	done := runManager(t, sm)

	waitForHTTPListening(t, mainAddr, 5*time.Second)

	slowBody := make(chan string, 1)

	go func() {
		resp, err := http.Get("http://" + mainAddr + "/slow")
		if err != nil {
			slowBody <- "error: " + err.Error()

			return
		}

		defer func() { _ = resp.Body.Close() }()

		body, _ := io.ReadAll(resp.Body)
		slowBody <- string(body)
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the slow API request never reached its handler")
	}

	close(shutdown)

	// The API listener closing is the sign the drain has begun; the slow
	// request still holds the API server open.
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", mainAddr, 100*time.Millisecond)
		if err != nil {
			return true
		}

		_ = conn.Close()

		return false
	}, 5*time.Second, 10*time.Millisecond, "the API listener must close once shutdown starts")

	assert.Equal(t, "admin", fetchProbe(t, adminAddr),
		"the admin port must answer while the API is still draining")

	close(release)

	assert.Equal(t, "api", <-slowBody, "the in-flight API request must complete")

	select {
	case err := <-done:
		require.NoError(t, err, "graceful shutdown must not report an error")
	case <-time.After(10 * time.Second):
		t.Fatal("manager did not finish its graceful shutdown within 10s")
	}
}

// TestAdminHTTPServerServesBesideAStdlibMainServer pins the composition the
// godoc of WithAdminHTTPServer promises for a service that owns its own stdlib
// listener: both ports answer, each from its own handler.
func TestAdminHTTPServerServesBesideAStdlibMainServer(t *testing.T) {
	listener := newTestStdlibListener(t)
	mainAddr := listener.Addr().String()
	adminAddr := reserveFreeAddr(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/probe", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("api"))
	})

	shutdown := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithStdlibHTTPListener(newTestStdlibServer(mainAddr, mux), listener).
		WithAdminHTTPServer(echoApp("admin"), adminAddr).
		WithShutdownChannel(shutdown).
		WithShutdownTimeout(5 * time.Second)

	done := runManager(t, sm)

	assert.Equal(t, "api", fetchProbe(t, mainAddr))
	assert.Equal(t, "admin", fetchProbe(t, adminAddr))

	requireCleanExit(t, shutdown, done)
}

// TestAdminHTTPServerDrainsLastBesideGRPC is the three-server case: all three
// bind, and the admin port still drains after both the API and gRPC, so
// readiness probes keep being answered while they finish in-flight work.
func TestAdminHTTPServerDrainsLastBesideGRPC(t *testing.T) {
	order := &drainOrder{}

	mainApp := echoApp("api")
	mainApp.Hooks().OnPostShutdown(order.record("api"))

	adminApp := echoApp("admin")
	adminApp.Hooks().OnPostShutdown(order.record("admin"))

	mainAddr := reserveFreeAddr(t)
	grpcAddr := reserveFreeAddr(t)
	adminAddr := reserveFreeAddr(t)

	logger := &recordingLogger{}
	shutdown := make(chan struct{})

	sm := server.NewServerManager(nil, nil, logger).
		WithHTTPServer(mainApp, mainAddr).
		WithGRPCServer(grpc.NewServer(), grpcAddr).
		WithAdminHTTPServer(adminApp, adminAddr).
		WithShutdownChannel(shutdown).
		WithShutdownTimeout(5 * time.Second)

	done := runManager(t, sm)

	assert.Equal(t, "api", fetchProbe(t, mainAddr))
	assert.Equal(t, "admin", fetchProbe(t, adminAddr))
	waitForHTTPListening(t, grpcAddr, 5*time.Second)

	requireCleanExit(t, shutdown, done)

	assert.Equal(t, []string{"api", "admin"}, order.seen(),
		"the admin server must be the last one to finish draining")

	msgs := logger.getMessages()
	grpcIdx := indexOf(msgs, "Shutting down gRPC server...")
	adminIdx := indexOf(msgs, "Shutting down admin HTTP server...")

	require.GreaterOrEqual(t, grpcIdx, 0, "the gRPC shutdown banner must appear (messages=%v)", msgs)
	require.GreaterOrEqual(t, adminIdx, 0, "the admin shutdown banner must appear (messages=%v)", msgs)
	assert.Less(t, grpcIdx, adminIdx, "the admin port must close after the gRPC server")
}

// TestAdminHTTPServerStartupErrorSurfaces pins that a failed admin bind ends
// the process the same way a failed API bind does.
func TestAdminHTTPServerStartupErrorSurfaces(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	defer func() { _ = occupied.Close() }()

	sm := server.NewServerManager(nil, nil, nil).
		WithAdminHTTPServer(fiber.New(), occupied.Addr().String())

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case err := <-done:
		require.Error(t, err, "a failed admin bind must propagate")
		assert.Contains(t, err.Error(), "admin HTTP server")
	case <-time.After(10 * time.Second):
		t.Fatal("the admin startup error was not propagated within 10s")
	}
}
