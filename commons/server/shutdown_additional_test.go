//go:build unit

package server_test

import (
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/server"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// probeMux answers GET /probe with name, like echoApp does for fiber.
func probeMux(name string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/probe", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(name))
	})

	return mux
}

// TestAdditionalStdlibHTTPServerBesideFiberMainGRPCAndAdmin is the co-located
// process: a Fiber API, a stdlib channel listener, gRPC and the admin app all
// serve, and the additional listener drains first, the admin app last.
func TestAdditionalStdlibHTTPServerBesideFiberMainGRPCAndAdmin(t *testing.T) {
	listener := newTestStdlibListener(t)
	extraAddr := listener.Addr().String()
	mainAddr := reserveFreeAddr(t)
	grpcAddr := reserveFreeAddr(t)
	adminAddr := reserveFreeAddr(t)

	logger := &recordingLogger{}
	shutdown := make(chan struct{})

	sm := server.NewServerManager(nil, nil, logger).
		WithHTTPServer(echoApp("api"), mainAddr).
		WithAdditionalStdlibHTTPListener(newTestStdlibServer(extraAddr, probeMux("soap")), listener).
		WithGRPCServer(grpc.NewServer(), grpcAddr).
		WithAdminHTTPServer(echoApp("admin"), adminAddr).
		WithShutdownChannel(shutdown).
		WithShutdownTimeout(5 * time.Second)

	done := runManager(t, sm)

	assert.Equal(t, "api", fetchProbe(t, mainAddr))
	assert.Equal(t, "soap", fetchProbe(t, extraAddr))
	assert.Equal(t, "admin", fetchProbe(t, adminAddr))
	waitForHTTPListening(t, grpcAddr, 5*time.Second)

	requireCleanExit(t, shutdown, done)

	msgs := logger.getMessages()
	order := []string{
		"Shutting down additional HTTP server...",
		"Shutting down HTTP server...",
		"Shutting down gRPC server...",
		"Shutting down admin HTTP server...",
	}

	previous := -1

	for _, banner := range order {
		idx := indexOf(msgs, banner)
		require.GreaterOrEqual(t, idx, 0, "banner %q must appear (messages=%v)", banner, msgs)
		assert.Greater(t, idx, previous, "shutdown order must be %v (messages=%v)", order, msgs)
		previous = idx
	}
}

// TestAdditionalStdlibHTTPServerBesideAStdlibMainServer pins that the new slot
// composes with the stdlib main slot too: two net/http servers, one manager.
func TestAdditionalStdlibHTTPServerBesideAStdlibMainServer(t *testing.T) {
	mainAddr := reserveFreeAddr(t)
	extraAddr := reserveFreeAddr(t)
	shutdown := make(chan struct{})

	extra := &http.Server{Addr: extraAddr, Handler: probeMux("soap")}

	sm := server.NewServerManager(nil, nil, nil).
		WithStdlibHTTPServer(newTestStdlibServer(mainAddr, probeMux("api"))).
		WithAdditionalStdlibHTTPServer(extra).
		WithShutdownChannel(shutdown).
		WithShutdownTimeout(5 * time.Second)

	assert.Equal(t, 5*time.Second, extra.ReadHeaderTimeout,
		"a zero ReadHeaderTimeout must get the same default as the main stdlib slot")

	done := runManager(t, sm)

	assert.Equal(t, "api", fetchProbe(t, mainAddr))
	assert.Equal(t, "soap", fetchProbe(t, extraAddr))

	requireCleanExit(t, shutdown, done)
}

// TestAdditionalStdlibHTTPServerAddressConflict pins that the additional
// server's address is checked against every other slot, before any goroutine.
func TestAdditionalStdlibHTTPServerAddressConflict(t *testing.T) {
	others := map[string]func(sm *server.ServerManager, addr string) *server.ServerManager{
		"fiber main": func(sm *server.ServerManager, addr string) *server.ServerManager {
			return sm.WithHTTPServer(fiber.New(), addr)
		},
		"stdlib main": func(sm *server.ServerManager, addr string) *server.ServerManager {
			return sm.WithStdlibHTTPServer(newTestStdlibServer(addr, http.NewServeMux()))
		},
		"admin": func(sm *server.ServerManager, addr string) *server.ServerManager {
			return sm.WithAdminHTTPServer(fiber.New(), addr)
		},
		"gRPC": func(sm *server.ServerManager, addr string) *server.ServerManager {
			return sm.WithGRPCServer(grpc.NewServer(), addr)
		},
	}

	spellings := map[string]func(string) string{
		"same string":   func(addr string) string { return addr },
		"wildcard host": func(addr string) string { return addr[strings.LastIndex(addr, ":"):] },
	}

	for name, configure := range others {
		for spelling, spell := range spellings {
			t.Run(name+", "+spelling, func(t *testing.T) {
				listener := newTestStdlibListener(t)
				bound := listener.Addr().String()

				sm := configure(server.NewServerManager(nil, nil, nil), spell(bound)).
					WithAdditionalStdlibHTTPListener(newTestStdlibServer(bound, http.NewServeMux()), listener)

				require.ErrorIs(t, sm.StartWithGracefulShutdownWithError(), server.ErrAdditionalHTTPAddressConflict)

				select {
				case <-sm.ServersStarted():
					t.Fatal("no server goroutine may be launched when the additional address conflicts")
				default:
				}
			})
		}
	}
}

// TestAdditionalStdlibHTTPServerKeepsMainSlotExclusion pins that the new slot
// lifts nothing else: Fiber main plus stdlib main is still refused.
func TestAdditionalStdlibHTTPServerKeepsMainSlotExclusion(t *testing.T) {
	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(fiber.New(), reserveFreeAddr(t)).
		WithStdlibHTTPServer(newTestStdlibServer(reserveFreeAddr(t), http.NewServeMux())).
		WithAdditionalStdlibHTTPServer(newTestStdlibServer(reserveFreeAddr(t), http.NewServeMux()))

	require.ErrorIs(t, sm.StartWithGracefulShutdownWithError(), server.ErrConflictingHTTPServers)
}

// TestAdditionalStdlibHTTPServerStartupErrorSurfaces pins that a failed bind
// of the additional server ends the manager with its own prefix.
func TestAdditionalStdlibHTTPServerStartupErrorSurfaces(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	defer func() { _ = occupied.Close() }()

	sm := server.NewServerManager(nil, nil, nil).
		WithAdditionalStdlibHTTPServer(newTestStdlibServer(occupied.Addr().String(), http.NewServeMux()))

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	select {
	case err := <-done:
		require.Error(t, err, "a failed additional bind must propagate")
		assert.True(t, strings.HasPrefix(err.Error(), "additional HTTP server: "), "got %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the additional startup error was not propagated within 10s")
	}
}

func TestWithAdditionalStdlibHTTPServer_NilReceiver(t *testing.T) {
	var sm *server.ServerManager

	assert.Nil(t, sm.WithAdditionalStdlibHTTPServer(&http.Server{}))
	assert.Nil(t, sm.WithAdditionalStdlibHTTPListener(&http.Server{}, nil))
}
