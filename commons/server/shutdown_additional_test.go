//go:build unit

package server_test

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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
// serve. The additional and main HTTP servers drain (in no guaranteed order
// between them) before gRPC, and the admin app drains last.
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
	grpcIdx := indexOf(msgs, "Shutting down gRPC server...")
	adminIdx := indexOf(msgs, "Shutting down admin HTTP server...")

	require.GreaterOrEqual(t, grpcIdx, 0, "messages=%v", msgs)
	assert.Greater(t, adminIdx, grpcIdx, "the admin app must drain last (messages=%v)", msgs)

	for _, banner := range []string{"Shutting down additional HTTP server...", "Shutting down HTTP server..."} {
		idx := indexOf(msgs, banner)
		require.GreaterOrEqual(t, idx, 0, "banner %q must appear (messages=%v)", banner, msgs)
		assert.Less(t, idx, grpcIdx, "%q must come before gRPC (messages=%v)", banner, msgs)
	}
}

// closeTimeListener records when the server under test first closed it,
// which http.Server.Shutdown does as its first act.
type closeTimeListener struct {
	net.Listener
	once     sync.Once
	closedAt atomic.Int64
}

func (l *closeTimeListener) Close() error {
	l.once.Do(func() { l.closedAt.Store(time.Now().UnixNano()) })

	return l.Listener.Close()
}

// hangingMux answers /hang only when the request context ends, which for an
// in-flight request happens when the server hard-closes its connection. It
// signals entered once a request is being held.
func hangingMux(entered chan<- struct{}, released chan<- time.Time) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/hang", func(_ http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-r.Context().Done()
		released <- time.Now()
	})

	return mux
}

// holdRequest issues GET path against addr in the background and waits until
// the handler signals it is holding the request.
func holdRequest(t *testing.T, addr, path string, entered <-chan struct{}) {
	t.Helper()

	waitForHTTPListening(t, addr, 5*time.Second)

	go func() {
		resp, err := http.Get("http://" + addr + path) //nolint:noctx // the server ends this request
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not reach its handler within 5s")
	}
}

// TestAdditionalStdlibHTTPServerSharesOneDrainBudgetWithMain measures that a
// request stuck on the additional server neither delays the main server's
// drain nor stretches the pair past one shutdownTimeout.
func TestAdditionalStdlibHTTPServerSharesOneDrainBudgetWithMain(t *testing.T) {
	const budget = 1500 * time.Millisecond

	mainListener := &closeTimeListener{Listener: newTestStdlibListener(t)}
	mainAddr := mainListener.Addr().String()
	extraAddr := reserveFreeAddr(t)

	entered := make(chan struct{}, 1)
	released := make(chan time.Time, 1)
	extraShutdownAt := make(chan time.Time, 1)

	extra := newTestStdlibServer(extraAddr, hangingMux(entered, released))
	extra.RegisterOnShutdown(func() { extraShutdownAt <- time.Now() })

	shutdown := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithStdlibHTTPListener(newTestStdlibServer(mainAddr, probeMux("api")), mainListener).
		WithAdditionalStdlibHTTPServer(extra).
		WithShutdownChannel(shutdown).
		WithShutdownTimeout(budget)

	done := runManager(t, sm)

	assert.Equal(t, "api", fetchProbe(t, mainAddr))
	holdRequest(t, extraAddr, "/hang", entered)

	start := time.Now()
	close(shutdown)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("manager did not finish its graceful shutdown within 10s")
	}

	elapsed := time.Since(start)

	closedAt := mainListener.closedAt.Load()
	require.NotZero(t, closedAt, "the main server was never shut down")
	assert.Less(t, time.Unix(0, closedAt).Sub(start), 500*time.Millisecond,
		"a stuck additional request must not delay the main drain")

	select {
	case at := <-extraShutdownAt:
		assert.Less(t, at.Sub(start), 500*time.Millisecond, "the additional drain must start at once")
	default:
		t.Fatal("the additional server's Shutdown was never invoked")
	}

	select {
	case at := <-released:
		assert.GreaterOrEqual(t, at.Sub(start), budget-100*time.Millisecond,
			"the stuck request is hard-closed only when the budget runs out")
	case <-time.After(5 * time.Second):
		t.Fatal("the stuck additional request was never released")
	}

	assert.GreaterOrEqual(t, elapsed, budget-100*time.Millisecond)
	assert.Less(t, elapsed, 2*budget, "additional plus main must fit in ONE shutdownTimeout")
}

// TestAdditionalStdlibHTTPServerMainFinishesInFlightWhileAdditionalDrains pins
// that a main request in flight at shutdown is answered while the additional
// server is still stuck in its drain.
func TestAdditionalStdlibHTTPServerMainFinishesInFlightWhileAdditionalDrains(t *testing.T) {
	const budget = 1500 * time.Millisecond

	mainAddr := reserveFreeAddr(t)
	extraAddr := reserveFreeAddr(t)

	mainEntered := make(chan struct{}, 1)
	mainMux := http.NewServeMux()
	mainMux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		mainEntered <- struct{}{}
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("api"))
	})

	entered := make(chan struct{}, 1)
	released := make(chan time.Time, 1)
	shutdown := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithStdlibHTTPServer(newTestStdlibServer(mainAddr, mainMux)).
		WithAdditionalStdlibHTTPServer(newTestStdlibServer(extraAddr, hangingMux(entered, released))).
		WithShutdownChannel(shutdown).
		WithShutdownTimeout(budget)

	done := runManager(t, sm)

	holdRequest(t, extraAddr, "/hang", entered)

	type answer struct {
		body string
		err  error
	}

	mainAnswer := make(chan answer, 1)

	go func() {
		resp, err := http.Get("http://" + mainAddr + "/slow") //nolint:noctx // bounded by the handler
		if err != nil {
			mainAnswer <- answer{err: err}
			return
		}

		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		mainAnswer <- answer{body: string(body), err: err}
	}()

	select {
	case <-mainEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the main request did not reach its handler within 5s")
	}

	start := time.Now()
	close(shutdown)

	select {
	case got := <-mainAnswer:
		require.NoError(t, got.err)
		assert.Equal(t, "api", got.body)
		assert.Less(t, time.Since(start), budget, "the main answer must not wait for the additional drain")
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight main request was never answered")
	}

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("manager did not finish its graceful shutdown within 10s")
	}

	<-released
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

// TestAdditionalStdlibHTTPServerSecondServerIsRefused pins that the single
// slot never silently replaces a server, through either option.
func TestAdditionalStdlibHTTPServerSecondServerIsRefused(t *testing.T) {
	cases := map[string]func(sm *server.ServerManager) *server.ServerManager{
		"server then server": func(sm *server.ServerManager) *server.ServerManager {
			return sm.WithAdditionalStdlibHTTPServer(newTestStdlibServer(reserveFreeAddr(t), http.NewServeMux())).
				WithAdditionalStdlibHTTPServer(newTestStdlibServer(reserveFreeAddr(t), http.NewServeMux()))
		},
		"server then listener": func(sm *server.ServerManager) *server.ServerManager {
			listener := newTestStdlibListener(t)

			return sm.WithAdditionalStdlibHTTPServer(newTestStdlibServer(reserveFreeAddr(t), http.NewServeMux())).
				WithAdditionalStdlibHTTPListener(newTestStdlibServer(listener.Addr().String(), http.NewServeMux()), listener)
		},
		"listener then server": func(sm *server.ServerManager) *server.ServerManager {
			listener := newTestStdlibListener(t)

			return sm.WithAdditionalStdlibHTTPListener(newTestStdlibServer(listener.Addr().String(), http.NewServeMux()), listener).
				WithAdditionalStdlibHTTPServer(newTestStdlibServer(reserveFreeAddr(t), http.NewServeMux()))
		},
	}

	for name, configure := range cases {
		t.Run(name, func(t *testing.T) {
			sm := configure(server.NewServerManager(nil, nil, nil))

			require.ErrorIs(t, sm.StartWithGracefulShutdownWithError(), server.ErrAdditionalHTTPServerAlreadyConfigured)

			select {
			case <-sm.ServersStarted():
				t.Fatal("no server goroutine may be launched when the slot was filled twice")
			default:
			}
		})
	}
}

// TestAdditionalStdlibHTTPServerNilClearsTheSlot pins that nil empties the
// slot, and that a server given after the clear fills an empty slot.
func TestAdditionalStdlibHTTPServerNilClearsTheSlot(t *testing.T) {
	t.Run("cleared slot leaves no servers", func(t *testing.T) {
		sm := server.NewServerManager(nil, nil, nil).
			WithAdditionalStdlibHTTPServer(newTestStdlibServer(reserveFreeAddr(t), http.NewServeMux())).
			WithAdditionalStdlibHTTPServer(nil)

		err := sm.StartWithGracefulShutdownWithError()
		require.ErrorIs(t, err, server.ErrNoServersConfigured)
		assert.Contains(t, err.Error(), "no servers configured")
	})

	t.Run("server after the clear serves", func(t *testing.T) {
		addr := reserveFreeAddr(t)
		shutdown := make(chan struct{})

		sm := server.NewServerManager(nil, nil, nil).
			WithAdditionalStdlibHTTPServer(newTestStdlibServer(reserveFreeAddr(t), probeMux("old"))).
			WithAdditionalStdlibHTTPServer(nil).
			WithAdditionalStdlibHTTPServer(newTestStdlibServer(addr, probeMux("soap"))).
			WithShutdownChannel(shutdown).
			WithShutdownTimeout(5 * time.Second)

		done := runManager(t, sm)

		assert.Equal(t, "soap", fetchProbe(t, addr))

		requireCleanExit(t, shutdown, done)
	})
}

// TestEphemeralPortsNeverConflict pins that two ":0" addresses are two
// kernel-chosen ports, not one socket, for both address checks.
func TestEphemeralPortsNeverConflict(t *testing.T) {
	cases := map[string]func(sm *server.ServerManager) *server.ServerManager{
		"additional beside stdlib main": func(sm *server.ServerManager) *server.ServerManager {
			return sm.WithStdlibHTTPServer(newTestStdlibServer("127.0.0.1:0", http.NewServeMux())).
				WithAdditionalStdlibHTTPServer(newTestStdlibServer("127.0.0.1:0", http.NewServeMux()))
		},
		"admin beside fiber main": func(sm *server.ServerManager) *server.ServerManager {
			return sm.WithHTTPServer(fiber.New(), "127.0.0.1:0").
				WithAdminHTTPServer(fiber.New(), "127.0.0.1:0")
		},
	}

	for name, configure := range cases {
		t.Run(name, func(t *testing.T) {
			shutdown := make(chan struct{})

			sm := configure(server.NewServerManager(nil, nil, nil)).
				WithShutdownChannel(shutdown).
				WithShutdownTimeout(5 * time.Second)

			requireCleanExit(t, shutdown, runManager(t, sm))
		})
	}
}

// TestEmptyStdlibAddressIsCheckedAsPort80 pins that an empty stdlib Addr,
// which net/http binds as ":http", is compared as port 80 by both checks
// instead of being skipped.
func TestEmptyStdlibAddressIsCheckedAsPort80(t *testing.T) {
	cases := map[string]struct {
		configure func(sm *server.ServerManager) *server.ServerManager
		want      error
	}{
		"additional empty vs stdlib main :80": {
			configure: func(sm *server.ServerManager) *server.ServerManager {
				return sm.WithStdlibHTTPServer(newTestStdlibServer(":80", http.NewServeMux())).
					WithAdditionalStdlibHTTPServer(newTestStdlibServer("", http.NewServeMux()))
			},
			want: server.ErrAdditionalHTTPAddressConflict,
		},
		"additional :80 vs stdlib main empty": {
			configure: func(sm *server.ServerManager) *server.ServerManager {
				return sm.WithStdlibHTTPServer(newTestStdlibServer("", http.NewServeMux())).
					WithAdditionalStdlibHTTPServer(newTestStdlibServer(":80", http.NewServeMux()))
			},
			want: server.ErrAdditionalHTTPAddressConflict,
		},
		"additional empty vs gRPC :80": {
			configure: func(sm *server.ServerManager) *server.ServerManager {
				return sm.WithGRPCServer(grpc.NewServer(), ":80").
					WithAdditionalStdlibHTTPServer(newTestStdlibServer("", http.NewServeMux()))
			},
			want: server.ErrAdditionalHTTPAddressConflict,
		},
		"admin :80 vs stdlib main empty": {
			configure: func(sm *server.ServerManager) *server.ServerManager {
				return sm.WithStdlibHTTPServer(newTestStdlibServer("", http.NewServeMux())).
					WithAdminHTTPServer(fiber.New(), ":80")
			},
			want: server.ErrAdminAddressConflict,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sm := tc.configure(server.NewServerManager(nil, nil, nil))

			require.ErrorIs(t, sm.StartWithGracefulShutdownWithError(), tc.want)
		})
	}
}

// TestAdditionalStdlibHTTPServerNilHandlerIsRefused pins that the additional
// slot never falls back to http.DefaultServeMux, through either option.
func TestAdditionalStdlibHTTPServerNilHandlerIsRefused(t *testing.T) {
	cases := map[string]func(sm *server.ServerManager) *server.ServerManager{
		"server": func(sm *server.ServerManager) *server.ServerManager {
			return sm.WithAdditionalStdlibHTTPServer(newTestStdlibServer(reserveFreeAddr(t), nil))
		},
		"listener": func(sm *server.ServerManager) *server.ServerManager {
			listener := newTestStdlibListener(t)

			return sm.WithAdditionalStdlibHTTPListener(newTestStdlibServer(listener.Addr().String(), nil), listener)
		},
	}

	for name, configure := range cases {
		t.Run(name, func(t *testing.T) {
			sm := configure(server.NewServerManager(nil, nil, nil))

			require.ErrorIs(t, sm.StartWithGracefulShutdownWithError(), server.ErrAdditionalHTTPHandlerMissing)

			select {
			case <-sm.ServersStarted():
				t.Fatal("no server goroutine may be launched without an additional handler")
			default:
			}
		})
	}
}

// TestAdditionalStdlibHTTPServerAlone pins that the additional slot is a
// server in its own right: a manager with nothing else serves and exits
// cleanly.
func TestAdditionalStdlibHTTPServerAlone(t *testing.T) {
	addr := reserveFreeAddr(t)
	shutdown := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithAdditionalStdlibHTTPServer(newTestStdlibServer(addr, probeMux("soap"))).
		WithShutdownChannel(shutdown).
		WithShutdownTimeout(5 * time.Second)

	done := runManager(t, sm)

	assert.Equal(t, "soap", fetchProbe(t, addr))

	requireCleanExit(t, shutdown, done)
}

// TestAdditionalStdlibHTTPServerBesideAStdlibListenerMain pins the
// composition with the pre-bound stdlib main variant.
func TestAdditionalStdlibHTTPServerBesideAStdlibListenerMain(t *testing.T) {
	mainListener := newTestStdlibListener(t)
	mainAddr := mainListener.Addr().String()
	extraAddr := reserveFreeAddr(t)
	shutdown := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithStdlibHTTPListener(newTestStdlibServer(mainAddr, probeMux("api")), mainListener).
		WithAdditionalStdlibHTTPServer(newTestStdlibServer(extraAddr, probeMux("soap"))).
		WithShutdownChannel(shutdown).
		WithShutdownTimeout(5 * time.Second)

	done := runManager(t, sm)

	assert.Equal(t, "api", fetchProbe(t, mainAddr))
	assert.Equal(t, "soap", fetchProbe(t, extraAddr))

	requireCleanExit(t, shutdown, done)
}

// TestAdditionalStdlibHTTPServerReadHeaderTimeout pins the Slowloris default
// on both variants, and that a caller's own value is never overwritten.
func TestAdditionalStdlibHTTPServerReadHeaderTimeout(t *testing.T) {
	variants := map[string]func(sm *server.ServerManager, srv *http.Server) *server.ServerManager{
		"server": func(sm *server.ServerManager, srv *http.Server) *server.ServerManager {
			return sm.WithAdditionalStdlibHTTPServer(srv)
		},
		"listener": func(sm *server.ServerManager, srv *http.Server) *server.ServerManager {
			return sm.WithAdditionalStdlibHTTPListener(srv, newTestStdlibListener(t))
		},
	}

	for name, configure := range variants {
		t.Run(name+", zero gets the default", func(t *testing.T) {
			srv := &http.Server{Handler: http.NewServeMux()}
			configure(server.NewServerManager(nil, nil, nil), srv)

			assert.Equal(t, 5*time.Second, srv.ReadHeaderTimeout)
		})

		t.Run(name+", preset is preserved", func(t *testing.T) {
			srv := &http.Server{Handler: http.NewServeMux(), ReadHeaderTimeout: time.Second}
			configure(server.NewServerManager(nil, nil, nil), srv)

			assert.Equal(t, time.Second, srv.ReadHeaderTimeout)
		})
	}
}

// hangingFiberApp answers /hang only once release is closed, signalling
// entered when it starts holding a request.
func hangingFiberApp(entered chan<- struct{}, release <-chan struct{}) *fiber.App {
	app := fiber.New()

	app.Get("/hang", func(c fiber.Ctx) error {
		entered <- struct{}{}
		<-release

		return c.SendString("released")
	})

	return app
}

// awaitManager waits for the manager's result and returns how long it took
// from start.
func awaitManager(t *testing.T, done <-chan error, start time.Time) time.Duration {
	t.Helper()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("manager did not return within 10s: a Fiber drain is unbounded")
	}

	return time.Since(start)
}

// TestFiberMainDrainIsBoundedByTheSharedBudget measures that a request hung
// on the Fiber main server holds the shutdown for one budget, not forever,
// while the additional stdlib server drains beside it.
func TestFiberMainDrainIsBoundedByTheSharedBudget(t *testing.T) {
	const budget = time.Second

	mainAddr := reserveFreeAddr(t)
	extraAddr := reserveFreeAddr(t)

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	extraShutdownAt := make(chan time.Time, 1)
	extra := newTestStdlibServer(extraAddr, probeMux("soap"))
	extra.RegisterOnShutdown(func() { extraShutdownAt <- time.Now() })

	shutdown := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(hangingFiberApp(entered, release), mainAddr).
		WithAdditionalStdlibHTTPServer(extra).
		WithShutdownChannel(shutdown).
		WithShutdownTimeout(budget)

	done := runManager(t, sm)

	assert.Equal(t, "soap", fetchProbe(t, extraAddr))
	holdRequest(t, mainAddr, "/hang", entered)

	start := time.Now()
	close(shutdown)

	elapsed := awaitManager(t, done, start)
	t.Logf("manager returned after %v with a hung Fiber main request (budget %v)", elapsed, budget)

	assert.GreaterOrEqual(t, elapsed, budget-100*time.Millisecond, "the hung request is waited on for the budget")
	assert.Less(t, elapsed, budget+800*time.Millisecond, "the Fiber main drain must end with the shared budget")

	select {
	case at := <-extraShutdownAt:
		assert.Less(t, at.Sub(start), 500*time.Millisecond, "the additional drain must run beside the Fiber main drain")
	default:
		t.Fatal("the additional server's Shutdown was never invoked")
	}
}

// TestFiberAdminDrainIsBoundedByItsBudget measures the same bound on the
// admin app, which drains last under its own budget.
func TestFiberAdminDrainIsBoundedByItsBudget(t *testing.T) {
	const budget = time.Second

	extraAddr := reserveFreeAddr(t)
	adminAddr := reserveFreeAddr(t)

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	shutdown := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithAdditionalStdlibHTTPServer(newTestStdlibServer(extraAddr, probeMux("soap"))).
		WithAdminHTTPServer(hangingFiberApp(entered, release), adminAddr).
		WithShutdownChannel(shutdown).
		WithShutdownTimeout(budget)

	done := runManager(t, sm)

	assert.Equal(t, "soap", fetchProbe(t, extraAddr))
	holdRequest(t, adminAddr, "/hang", entered)

	start := time.Now()
	close(shutdown)

	elapsed := awaitManager(t, done, start)
	t.Logf("manager returned after %v with a hung admin request (budget %v)", elapsed, budget)

	assert.GreaterOrEqual(t, elapsed, budget-100*time.Millisecond, "the hung request is waited on for the budget")
	assert.Less(t, elapsed, budget+800*time.Millisecond, "the admin drain must end with its budget")
}

// TestFiberAbandonedConnectionKeepsServingPastTheDeadline pins a documented
// limit, not a wish: past the drain deadline fasthttp abandons an active
// Fiber connection instead of closing it, so the released request is answered
// without "Connection: close" and the same keep-alive connection serves a
// further request after the manager has returned. If a fasthttp change starts
// closing it, this test fails and the documentation must follow.
func TestFiberAbandonedConnectionKeepsServingPastTheDeadline(t *testing.T) {
	const budget = time.Second

	mainAddr := reserveFreeAddr(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})

	app := hangingFiberApp(entered, release)
	app.Get("/probe", func(c fiber.Ctx) error { return c.SendString("late") })

	shutdown := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(app, mainAddr).
		WithShutdownChannel(shutdown).
		WithShutdownTimeout(budget)

	done := runManager(t, sm)

	waitForHTTPListening(t, mainAddr, 5*time.Second)

	conn, err := net.Dial("tcp", mainAddr)
	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))

	reader := bufio.NewReader(conn)
	send := func(path string) {
		t.Helper()

		_, err := conn.Write([]byte("GET " + path + " HTTP/1.1\r\nHost: " + mainAddr + "\r\n\r\n"))
		require.NoError(t, err)
	}
	read := func() (*http.Response, string) {
		t.Helper()

		resp, err := http.ReadResponse(reader, nil)
		require.NoError(t, err)

		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		return resp, string(body)
	}

	send("/hang")

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not reach its handler within 5s")
	}

	start := time.Now()
	close(shutdown)

	elapsed := awaitManager(t, done, start)
	assert.Less(t, elapsed, budget+800*time.Millisecond)

	close(release)

	resp, body := read()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "released", body)
	assert.False(t, resp.Close, "the abandoned connection is not told to close")

	send("/probe")

	resp, body = read()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the abandoned connection still serves after the manager returned")
	assert.Equal(t, "late", body)
}
