package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	"github.com/LerianStudio/lib-commons/v7/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-commons/v7/commons/license"
	"github.com/LerianStudio/lib-observability/v4/runtime"
	"github.com/gofiber/fiber/v3"
	"google.golang.org/grpc"
)

// ErrNoServersConfigured indicates no servers were configured for the manager.
var ErrNoServersConfigured = errors.New("no servers configured: use WithHTTPServer(), WithStdlibHTTPServer(), WithStdlibHTTPListener(), WithAdditionalStdlibHTTPServer(), WithAdditionalStdlibHTTPListener(), WithGRPCServer(), or WithAdminHTTPServer()")

// ErrConflictingHTTPServers indicates that Fiber HTTP and stdlib HTTP were both
// configured on the same ServerManager. WithHTTPServer(*fiber.App) is mutually
// exclusive with WithStdlibHTTPServer(*http.Server) and
// WithStdlibHTTPListener(*http.Server, net.Listener).
var ErrConflictingHTTPServers = errors.New("conflicting HTTP servers configured: WithHTTPServer(*fiber.App) is mutually exclusive with WithStdlibHTTPServer(*http.Server) and WithStdlibHTTPListener(*http.Server, net.Listener)")

// ErrAdminAddressConflict indicates the admin HTTP server was configured on
// the same address as the main HTTP server. Both would bind the same socket,
// so the admin port must have an address of its own.
var ErrAdminAddressConflict = errors.New("conflicting HTTP addresses configured: WithAdminHTTPServer() needs an address different from the main HTTP server")

// ErrAdditionalHTTPAddressConflict indicates the additional stdlib HTTP server
// was configured on the same address as another server of the manager (main
// HTTP, admin HTTP or gRPC). Both would bind the same socket, so the additional
// server must have an address of its own.
var ErrAdditionalHTTPAddressConflict = errors.New("conflicting HTTP addresses configured: WithAdditionalStdlibHTTPServer() needs an address different from every other server")

// ErrAdditionalHTTPServerAlreadyConfigured indicates the additional stdlib
// HTTP slot was given a server while it already held one. The slot holds a
// single server by design; a second stdlib surface goes on the main slot.
var ErrAdditionalHTTPServerAlreadyConfigured = errors.New("additional HTTP server already configured: WithAdditionalStdlibHTTPServer() and WithAdditionalStdlibHTTPListener() accept one server; put another stdlib server on the main slot")

const defaultReadHeaderTimeout = 5 * time.Second

// ServerManager handles the graceful shutdown of multiple server types.
// It can manage a main HTTP server (either *fiber.App or stdlib *http.Server,
// but not both), an additional stdlib *http.Server, gRPC servers, the admin
// app, or any compatible combination simultaneously.
type ServerManager struct {
	httpServer          *fiber.App
	adminServer         *fiber.App
	stdlibHTTPServer    *http.Server
	stdlibHTTPListener  net.Listener
	additionalHTTP      *http.Server
	additionalListener  net.Listener
	additionalReplaced  bool
	grpcServer          *grpc.Server
	licenseClient       *license.ManagerShutdown
	telemetry           obs.TelemetryShutdowner
	logger              obs.Logger
	httpAddress         string
	adminAddress        string
	grpcAddress         string
	serversStarted      chan struct{}
	serversStartedOnce  sync.Once
	runtimeDefaultsOnce sync.Once
	shutdownChan        <-chan struct{}
	lifecycleMu         sync.Mutex
	shuttingDown        bool
	fiberListenDone     chan struct{}
	adminListenDone     chan struct{}
	shutdownOnce        sync.Once
	shutdownTimeout     time.Duration
	startupErrors       chan error
	shutdownHooks       []func(context.Context) error
}

// ensureRuntimeDefaults initializes zero-value fields so exported lifecycle
// methods remain nil-safe even when ServerManager is manually instantiated.
func (sm *ServerManager) ensureRuntimeDefaults() {
	if sm == nil {
		return
	}

	if nilcheck.Interface(sm.logger) {
		sm.logger = obs.Nop()
	}

	if sm.serversStarted == nil {
		sm.serversStarted = make(chan struct{})
	}

	if sm.startupErrors == nil {
		sm.startupErrors = make(chan error, 2)
	}
}

// NewServerManager creates a new instance of ServerManager.
// If logger is nil, a no-op logger is used to ensure nil-safe operation
// throughout the server lifecycle.
//
// telemetry is an obs.TelemetryShutdowner. Because that interface names no
// nominal type at all, *tracing.Telemetry from ANY lib-observability major
// satisfies it directly - pass yours as-is, no adapter needed.
func NewServerManager(
	licenseClient *license.ManagerShutdown,
	telemetry obs.TelemetryShutdowner,
	logger obs.Logger,
) *ServerManager {
	if nilcheck.Interface(logger) {
		logger = obs.Nop()
	}

	return &ServerManager{
		licenseClient:   licenseClient,
		telemetry:       telemetry,
		logger:          logger,
		serversStarted:  make(chan struct{}),
		shutdownTimeout: 30 * time.Second,
		startupErrors:   make(chan error, 2),
	}
}

// WithHTTPServer configures a Fiber HTTP server for the ServerManager.
//
// Mutually exclusive with WithStdlibHTTPServer: configuring both causes
// StartWithGracefulShutdownWithError to return ErrConflictingHTTPServers
// before any goroutine is launched.
func (sm *ServerManager) WithHTTPServer(app *fiber.App, address string) *ServerManager {
	if sm == nil {
		return nil
	}

	sm.httpServer = app
	sm.httpAddress = address

	return sm
}

// WithAdminHTTPServer configures the admin Fiber app, on its own port, with
// the same lifecycle as the main HTTP server: it is launched by the same
// supervised goroutine machinery and a failed bind surfaces on the same
// startup error path.
//
// The admin port carries the operational endpoints (/health, /readyz,
// /version, /metrics) and is never exposed through a Service or Ingress. It
// composes with any other slot - Fiber HTTP, stdlib HTTP, gRPC, or none at
// all - but its address must differ from the main HTTP address, otherwise
// StartWithGracefulShutdownWithError returns ErrAdminAddressConflict before
// any goroutine is launched.
//
// During shutdown the admin app is the LAST server to drain: it keeps
// answering, with whatever the service's handlers return, while the API and
// gRPC servers finish their in-flight work, and closes last. A service that
// wants /readyz to report 503 during the drain does so in its own handler.
func (sm *ServerManager) WithAdminHTTPServer(app *fiber.App, address string) *ServerManager {
	if sm == nil {
		return nil
	}

	sm.adminServer = app
	sm.adminAddress = address

	return sm
}

// WithStdlibHTTPServer configures a stdlib *http.Server for the ServerManager.
// The server's Addr and Handler fields are used as-is. If ReadHeaderTimeout is
// zero, a safe 5s default is installed to avoid Slowloris-prone servers while
// preserving any caller-supplied nonzero timeout. This is the variant for consumers that own a
// net/http server directly (custom ServeMux dispatch, SOAP/XML handlers, mock
// binaries) rather than a Fiber app.
//
// Behavior parity with WithHTTPServer:
//   - The launch goroutine is wrapped in runtime.SafeGoWithContextAndComponent
//     so panics route through the lib-commons panic observability trident.
//   - A non-nil return from srv.ListenAndServe surfaces on the same
//     startupErrors channel that the fiber path uses, and propagates as the
//     return value of StartWithGracefulShutdownWithError.
//   - http.ErrServerClosed (returned by ListenAndServe after a clean
//     Shutdown) is mapped to nil, mirroring fiber.App.Listen returning nil
//     after fiber.App.Shutdown.
//   - Graceful drain calls srv.Shutdown(ctx) with a context bounded by the
//     existing shutdownTimeout field (set via WithShutdownTimeout, default
//     30s). The drain ctx is independent of any caller cancellation so a
//     canceled parent does not abort the in-flight request drain.
//
// Mutually exclusive with WithHTTPServer: configuring both causes
// StartWithGracefulShutdownWithError to return ErrConflictingHTTPServers
// before any goroutine is launched.
func (sm *ServerManager) WithStdlibHTTPServer(srv *http.Server) *ServerManager {
	if sm == nil {
		return nil
	}

	if srv != nil && srv.ReadHeaderTimeout == 0 {
		srv.ReadHeaderTimeout = defaultReadHeaderTimeout
	}

	sm.stdlibHTTPServer = srv
	sm.stdlibHTTPListener = nil

	return sm
}

// WithStdlibHTTPListener configures a stdlib *http.Server with a caller-owned,
// pre-bound listener. It is useful for tests and socket-activation style
// bootstraps where the listener must be acquired before ServerManager starts.
// Shutdown semantics match WithStdlibHTTPServer: Server.Shutdown owns graceful
// drain and closes the listener during shutdown.
//
// Mutually exclusive with WithHTTPServer: configuring both causes
// StartWithGracefulShutdownWithError to return ErrConflictingHTTPServers
// before any goroutine is launched.
func (sm *ServerManager) WithStdlibHTTPListener(srv *http.Server, listener net.Listener) *ServerManager {
	if sm == nil {
		return nil
	}

	if srv == nil || listener == nil {
		return sm
	}

	if srv.ReadHeaderTimeout == 0 {
		srv.ReadHeaderTimeout = defaultReadHeaderTimeout
	}

	sm.stdlibHTTPServer = srv
	sm.stdlibHTTPListener = listener

	return sm
}

// WithAdditionalStdlibHTTPServer configures a second stdlib *http.Server, on
// its own port, beside whatever else the manager runs: a Fiber or stdlib main
// HTTP server, gRPC, and the admin app. It is the slot for a process that
// serves two channel-facing surfaces, one of them on plain net/http (a SOAP
// listener beside a Fiber API, for instance).
//
// It behaves like WithStdlibHTTPServer in every respect but two:
//   - It is not subject to ErrConflictingHTTPServers: it composes with either
//     main HTTP variant.
//   - At shutdown it drains CONCURRENTLY with the main HTTP server, with no
//     ordering guarantee between the two. Both drains start under one
//     shared shutdownTimeout budget, so together they take at most one
//     shutdownTimeout and a stuck request on one never delays the other's
//     drain; the additional server keeps the Shutdown-then-Close fallback.
//     (A Fiber main server keeps its own drain, which runs in parallel.)
//     gRPC and the admin app drain afterwards, each with its own budget.
//
// A zero ReadHeaderTimeout is upgraded to the same safe default, a failed
// bind surfaces on the shared startup error path prefixed "additional HTTP
// server", and its address must differ from every other configured server,
// otherwise StartWithGracefulShutdownWithError returns
// ErrAdditionalHTTPAddressConflict before any goroutine is launched.
//
// The slot holds ONE server. Giving it a non-nil server while it already
// holds one (through either additional option) makes
// StartWithGracefulShutdownWithError return
// ErrAdditionalHTTPServerAlreadyConfigured; a second stdlib surface goes on the
// main slot. A nil srv clears the slot, as it does for WithStdlibHTTPServer,
// and a later non-nil server then fills an empty slot.
func (sm *ServerManager) WithAdditionalStdlibHTTPServer(srv *http.Server) *ServerManager {
	if sm == nil {
		return nil
	}

	if srv == nil {
		sm.additionalHTTP = nil
		sm.additionalListener = nil
		sm.additionalReplaced = false

		return sm
	}

	if srv.ReadHeaderTimeout == 0 {
		srv.ReadHeaderTimeout = defaultReadHeaderTimeout
	}

	sm.additionalReplaced = sm.additionalReplaced || sm.additionalHTTP != nil
	sm.additionalHTTP = srv
	sm.additionalListener = nil

	return sm
}

// WithAdditionalStdlibHTTPListener is WithAdditionalStdlibHTTPServer with a
// caller-owned, pre-bound listener, as WithStdlibHTTPListener is to
// WithStdlibHTTPServer. A nil server or listener leaves the manager unchanged;
// a non-nil pair on an occupied slot is refused at start with
// ErrAdditionalHTTPServerAlreadyConfigured.
func (sm *ServerManager) WithAdditionalStdlibHTTPListener(srv *http.Server, listener net.Listener) *ServerManager {
	if sm == nil {
		return nil
	}

	if srv == nil || listener == nil {
		return sm
	}

	if srv.ReadHeaderTimeout == 0 {
		srv.ReadHeaderTimeout = defaultReadHeaderTimeout
	}

	sm.additionalReplaced = sm.additionalReplaced || sm.additionalHTTP != nil
	sm.additionalHTTP = srv
	sm.additionalListener = listener

	return sm
}

// WithGRPCServer configures the gRPC server for the ServerManager.
func (sm *ServerManager) WithGRPCServer(server *grpc.Server, address string) *ServerManager {
	if sm == nil {
		return nil
	}

	sm.grpcServer = server
	sm.grpcAddress = address

	return sm
}

// WithShutdownChannel configures a custom shutdown channel for the ServerManager.
// This allows tests to trigger shutdown deterministically instead of relying on OS signals.
func (sm *ServerManager) WithShutdownChannel(ch <-chan struct{}) *ServerManager {
	if sm == nil {
		return nil
	}

	sm.shutdownChan = ch

	return sm
}

// WithShutdownTimeout configures the maximum duration to wait for gRPC GracefulStop
// before forcing a hard stop. Defaults to 30 seconds.
func (sm *ServerManager) WithShutdownTimeout(d time.Duration) *ServerManager {
	if sm == nil {
		return nil
	}

	sm.shutdownTimeout = d

	return sm
}

// WithShutdownHook registers a function to be called during graceful shutdown.
// Hooks are executed in registration order, AFTER HTTP and gRPC servers have
// drained/stopped and BEFORE telemetry/logger/license shutdown. Each hook
// receives a context bounded by the shutdown timeout. Errors from hooks are
// logged but do not prevent subsequent hooks or the rest of the shutdown
// sequence from running (best-effort cleanup).
func (sm *ServerManager) WithShutdownHook(hook func(context.Context) error) *ServerManager {
	if sm == nil || hook == nil {
		return sm
	}

	sm.shutdownHooks = append(sm.shutdownHooks, hook)

	return sm
}

// ServersStarted returns a channel that is closed when server goroutines have been launched.
// Note: This signals that goroutines were spawned, not that sockets are bound and ready to accept connections.
// This is useful for tests to coordinate shutdown timing after server launch.
// Returns a closed channel on nil receiver to prevent callers from blocking forever.
func (sm *ServerManager) ServersStarted() <-chan struct{} {
	if sm == nil {
		ch := make(chan struct{})
		close(ch)

		return ch
	}

	sm.runtimeDefaultsOnce.Do(sm.ensureRuntimeDefaults)

	return sm.serversStarted
}

func (sm *ServerManager) validateConfiguration() error {
	// Mutual exclusion between the two HTTP variants must be checked BEFORE
	// the "no servers configured" check: a caller who supplied both servers
	// has a configuration bug worth surfacing precisely, not a "no servers"
	// false negative.
	if sm.httpServer != nil && sm.stdlibHTTPServer != nil {
		return ErrConflictingHTTPServers
	}

	if sm.adminServer != nil {
		if address := sm.mainHTTPAddress(); address != "" && sameListenAddress(address, sm.adminAddress) {
			return ErrAdminAddressConflict
		}
	}

	if err := sm.validateAdditionalHTTP(); err != nil {
		return err
	}

	if sm.httpServer == nil && sm.stdlibHTTPServer == nil && sm.additionalHTTP == nil && sm.grpcServer == nil && sm.adminServer == nil {
		return ErrNoServersConfigured
	}

	return nil
}

// validateAdditionalHTTP checks the additional stdlib slot: filled once, on
// an address of its own.
func (sm *ServerManager) validateAdditionalHTTP() error {
	if sm.additionalReplaced {
		return ErrAdditionalHTTPServerAlreadyConfigured
	}

	if sm.additionalHTTP == nil {
		return nil
	}

	address := stdlibAddress(sm.additionalHTTP, sm.additionalListener)

	for _, other := range []string{sm.mainHTTPAddress(), sm.adminAddressIfConfigured(), sm.grpcAddressIfConfigured()} {
		if address != "" && other != "" && sameListenAddress(address, other) {
			return ErrAdditionalHTTPAddressConflict
		}
	}

	return nil
}

// mainHTTPAddress returns the address the main HTTP server binds, whichever
// variant is configured, or "" when the manager serves no main HTTP traffic.
func (sm *ServerManager) mainHTTPAddress() string {
	switch {
	case sm.httpServer != nil:
		return sm.httpAddress
	case sm.stdlibHTTPServer != nil:
		return stdlibAddress(sm.stdlibHTTPServer, sm.stdlibHTTPListener)
	default:
		return ""
	}
}

// adminAddressIfConfigured returns the admin address, or "" without an admin app.
func (sm *ServerManager) adminAddressIfConfigured() string {
	if sm.adminServer == nil {
		return ""
	}

	return sm.adminAddress
}

// grpcAddressIfConfigured returns the gRPC address, or "" without a gRPC server.
func (sm *ServerManager) grpcAddressIfConfigured() string {
	if sm.grpcServer == nil {
		return ""
	}

	return sm.grpcAddress
}

// stdlibAddress returns the address a stdlib server binds: the pre-bound
// listener's when one was supplied, the server's Addr otherwise.
func stdlibAddress(srv *http.Server, listener net.Listener) string {
	if listener != nil {
		return listener.Addr().String()
	}

	return srv.Addr
}

// sameListenAddress reports whether two listen addresses would bind the same
// socket. Comparing the strings is not enough: a configured address is usually
// written ":8081", while the address of a pre-bound listener always comes back
// resolved as "127.0.0.1:8081", and a wildcard bind covers every host on that
// port anyway. So the port must match and either host must be a wildcard or
// the two hosts must be equal. Two spellings of one host (127.0.0.1 and
// localhost) are not resolved: the kernel catches that pair at bind time.
func sameListenAddress(a, b string) bool {
	hostA, portA, errA := net.SplitHostPort(a)
	hostB, portB, errB := net.SplitHostPort(b)

	if errA != nil || errB != nil {
		return a == b
	}

	if portA != portB {
		return false
	}

	return wildcardHost(hostA) || wildcardHost(hostB) || hostA == hostB
}

// wildcardHost reports whether a host part binds every interface.
func wildcardHost(host string) bool {
	return host == "" || host == "0.0.0.0" || host == "::"
}

// configuredServers counts the servers that will be launched, so the startup
// error channel is wide enough for every one of them to report a failed bind
// without blocking or losing its error.
func (sm *ServerManager) configuredServers() int {
	count := 0

	for _, configured := range []bool{
		sm.httpServer != nil,
		sm.stdlibHTTPServer != nil,
		sm.additionalHTTP != nil,
		sm.grpcServer != nil,
		sm.adminServer != nil,
	} {
		if configured {
			count++
		}
	}

	return count
}

// initServers validates configuration and starts servers without blocking.
// Returns an error if validation fails. Does not call Fatal.
func (sm *ServerManager) initServers() error {
	sm.runtimeDefaultsOnce.Do(sm.ensureRuntimeDefaults)

	if err := sm.validateConfiguration(); err != nil {
		return err
	}

	// Sized after validation and before any goroutine exists, so every launch
	// goroutine below observes this channel.
	sm.startupErrors = make(chan error, sm.configuredServers())

	sm.startServers()

	return nil
}

// StartWithGracefulShutdownWithError validates configuration and starts servers.
// Returns an error if no servers are configured instead of calling Fatal.
// Blocks until shutdown signal is received or shutdown channel is closed.
func (sm *ServerManager) StartWithGracefulShutdownWithError() error {
	if sm == nil {
		return ErrNoServersConfigured
	}

	sm.runtimeDefaultsOnce.Do(sm.ensureRuntimeDefaults)

	if err := sm.initServers(); err != nil {
		return err
	}

	return sm.handleShutdown()
}

// StartWithGracefulShutdown initializes all configured servers and sets up graceful shutdown.
// It terminates the process with os.Exit(1) if no servers are configured (backward compatible behavior).
// Note: On configuration error, logFatal always terminates the process regardless of logger availability.
// Use StartWithGracefulShutdownWithError() for proper error handling without process termination.
func (sm *ServerManager) StartWithGracefulShutdown() {
	if sm == nil {
		fmt.Println(ErrNoServersConfigured.Error())
		os.Exit(1)
	}

	sm.runtimeDefaultsOnce.Do(sm.ensureRuntimeDefaults)

	if err := sm.initServers(); err != nil {
		// logFatal exits the process via os.Exit(1); code below is unreachable on error
		sm.logFatal(err.Error())
	}

	// Run everything in a recover block
	defer func() {
		if r := recover(); r != nil {
			runtime.HandlePanicValue(context.Background(), sm.logger, r, "server", "StartWithGracefulShutdown")

			sm.executeShutdown()

			os.Exit(1)
		}
	}()

	_ = sm.handleShutdown()
}

// startServers starts all configured servers in separate goroutines.
// Note: Validation is performed by validateConfiguration() before this method is called.
// Callers using StartWithGracefulShutdown() directly will still get Fatal behavior for backward compatibility,
// while StartWithGracefulShutdownWithError() validates first and returns an error.
//
// Launch order is fiber HTTP → stdlib HTTP → additional stdlib HTTP → gRPC →
// admin HTTP. The two main HTTP branches are mutually exclusive (enforced by
// validateConfiguration) so at most one of them fires; the additional, gRPC
// and admin branches are independent and compose with either.
func (sm *ServerManager) startServers() {
	started := 0

	if sm.launchFiberHTTPServer() {
		started++
	}

	if sm.launchStdlibHTTPServer() {
		started++
	}

	if sm.launchAdditionalHTTPServer() {
		started++
	}

	if sm.launchGRPCServer() {
		started++
	}

	if sm.launchAdminHTTPServer() {
		started++
	}

	sm.logger.Log(context.Background(), obs.LevelInfo, "launched server goroutines", "count", started)

	// Signal that server goroutines have been launched (not that sockets are bound).
	sm.serversStartedOnce.Do(func() {
		close(sm.serversStarted)
	})
}

// launchFiberHTTPServer spawns the main fiber HTTP launch goroutine. Returns
// true if a goroutine was launched, false if no fiber server is configured.
func (sm *ServerManager) launchFiberHTTPServer() bool {
	return sm.launchFiberApp(sm.httpServer, sm.httpAddress, "HTTP", "start_http_server", &sm.fiberListenDone)
}

// launchAdminHTTPServer spawns the admin fiber launch goroutine. Returns true
// if a goroutine was launched, false if no admin server is configured.
func (sm *ServerManager) launchAdminHTTPServer() bool {
	return sm.launchFiberApp(sm.adminServer, sm.adminAddress, "admin HTTP", "start_admin_http_server", &sm.adminListenDone)
}

// launchFiberApp spawns the listen goroutine for a fiber app. label names the
// server in logs and in the startup error, operation names the supervised
// goroutine, and listenDone receives the lifecycle channel the shutdown path
// waits on. Returns false when the app is not configured, or when shutdown
// already began.
func (sm *ServerManager) launchFiberApp(app *fiber.App, address, label, operation string, listenDone *chan struct{}) bool {
	if app == nil {
		return false
	}

	// Publish the listen-goroutine lifecycle signal under the lifecycle lock
	// so a concurrent executeShutdown either observes the channel (and waits
	// on it) or has already set shuttingDown (and this launch is refused).
	// Without this ordering a shutdown racing startup could miss the channel
	// and let a later Listen keep serving after graceful shutdown completed.
	sm.lifecycleMu.Lock()

	if sm.shuttingDown {
		sm.lifecycleMu.Unlock()
		sm.logInfo("Skipping " + label + " server launch: shutdown already initiated")

		return false
	}

	*listenDone = make(chan struct{})
	done := *listenDone
	sm.lifecycleMu.Unlock()

	runtime.SafeGoWithContextAndComponent(
		context.Background(),
		sm.logger,
		"server",
		operation,
		runtime.KeepRunning,
		func(_ context.Context) {
			defer close(done)

			sm.logger.Log(context.Background(), obs.LevelInfo, "starting "+label+" server", "address", address)

			// DisableStartupMessage: fiber v3 moved banner suppression from
			// fiber.Config to ListenConfig, and this Listen call is the only
			// one the fleet reaches — without it every service prints the
			// fiber ASCII banner into its JSON-only stdout stream.
			if err := app.Listen(address, fiber.ListenConfig{DisableStartupMessage: true}); err != nil {
				sm.logger.Log(context.Background(), obs.LevelError, label+" server error", "error", err)

				select {
				case sm.startupErrors <- fmt.Errorf("%s server: %w", label, err):
				default:
				}
			}
		},
	)

	return true
}

// launchStdlibHTTPServer spawns the stdlib HTTP launch goroutine. Returns true
// if a goroutine was launched, false if no stdlib server is configured.
//
// Mutually exclusive with launchFiberHTTPServer at the configuration layer
// (validateConfiguration rejects the combination), so at most one of the two
// HTTP branches fires per process lifetime.
func (sm *ServerManager) launchStdlibHTTPServer() bool {
	return sm.launchStdlibServer(sm.stdlibHTTPServer, sm.stdlibHTTPListener, "stdlib HTTP", "HTTP server", "start_stdlib_http_server")
}

// launchAdditionalHTTPServer spawns the additional stdlib HTTP launch
// goroutine. Returns true if a goroutine was launched, false if no additional
// server is configured.
func (sm *ServerManager) launchAdditionalHTTPServer() bool {
	return sm.launchStdlibServer(sm.additionalHTTP, sm.additionalListener, "additional stdlib HTTP", "additional HTTP server", "start_additional_stdlib_http_server")
}

// launchStdlibServer spawns the serve goroutine for a stdlib server. label
// names the server in logs, errPrefix prefixes its startup error, and
// operation names the supervised goroutine.
func (sm *ServerManager) launchStdlibServer(srv *http.Server, listener net.Listener, label, errPrefix, operation string) bool {
	if srv == nil {
		return false
	}

	runtime.SafeGoWithContextAndComponent(
		context.Background(),
		sm.logger,
		"server",
		operation,
		runtime.KeepRunning,
		func(_ context.Context) {
			sm.logger.Log(context.Background(), obs.LevelInfo, "starting "+label+" server", "address", stdlibAddress(srv, listener))

			// ListenAndServe returns http.ErrServerClosed on a clean
			// Shutdown — that is the success signal, not an error.
			// Parity with fiber.App.Listen returning nil after
			// fiber.App.Shutdown.
			var err error
			if listener != nil {
				err = srv.Serve(listener)
			} else {
				err = srv.ListenAndServe()
			}

			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				sm.logger.Log(context.Background(), obs.LevelError, label+" server error", "error", err)

				select {
				case sm.startupErrors <- fmt.Errorf("%s: %w", errPrefix, err):
				default:
				}
			}
		},
	)

	return true
}

// launchGRPCServer spawns the gRPC launch goroutine. Returns true if a
// goroutine was launched, false if no gRPC server is configured.
func (sm *ServerManager) launchGRPCServer() bool {
	if sm.grpcServer == nil {
		return false
	}

	runtime.SafeGoWithContextAndComponent(
		context.Background(),
		sm.logger,
		"server",
		"start_grpc_server",
		runtime.KeepRunning,
		func(_ context.Context) {
			sm.logger.Log(context.Background(), obs.LevelInfo, "starting gRPC server", "address", sm.grpcAddress)

			listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", sm.grpcAddress)
			if err != nil {
				sm.logger.Log(context.Background(), obs.LevelError, "failed to listen on gRPC address", "error", err)

				select {
				case sm.startupErrors <- fmt.Errorf("gRPC listen: %w", err):
				default:
				}

				return
			}

			if err := sm.grpcServer.Serve(listener); err != nil {
				sm.logger.Log(context.Background(), obs.LevelError, "gRPC server error", "error", err)

				select {
				case sm.startupErrors <- fmt.Errorf("gRPC serve: %w", err):
				default:
				}
			}
		},
	)

	return true
}

// logInfo safely logs an info message if logger is available
func (sm *ServerManager) logInfo(msg string) {
	if !nilcheck.Interface(sm.logger) {
		sm.logger.Log(context.Background(), obs.LevelInfo, msg)
	}
}

// logFatal logs a fatal message and terminates the process with os.Exit(1).
// Uses Error level for logging to avoid relying on logger implementations
// that may or may not call os.Exit(1) in their Fatal method.
func (sm *ServerManager) logFatal(msg string) {
	if !nilcheck.Interface(sm.logger) {
		sm.logger.Log(context.Background(), obs.LevelError, msg)
	} else {
		fmt.Println(msg)
	}

	os.Exit(1)
}

// handleShutdown sets up signal handling and executes the shutdown sequence
// when a termination signal is received, when the shutdown channel is closed,
// or when a server startup error is detected.
// Returns the first startup error if one caused the shutdown, nil otherwise.
func (sm *ServerManager) handleShutdown() error {
	sm.runtimeDefaultsOnce.Do(sm.ensureRuntimeDefaults)

	var startupErr error

	if sm.shutdownChan != nil {
		select {
		case <-sm.shutdownChan:
		case err := <-sm.startupErrors:
			sm.logger.Log(context.Background(), obs.LevelError, "server startup failed", "error", err)

			startupErr = err
		}
	} else {
		c := make(chan os.Signal, 1)

		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(c)

		select {
		case <-c:
		case err := <-sm.startupErrors:
			sm.logger.Log(context.Background(), obs.LevelError, "server startup failed", "error", err)

			startupErr = err
		}
	}

	sm.logInfo("Gracefully shutting down all servers...")

	sm.executeShutdown()

	return startupErr
}

// executeShutdown performs the actual shutdown operations in the correct order for ServerManager.
// It is idempotent: multiple calls are safe, but only the first invocation executes the shutdown sequence.
func (sm *ServerManager) executeShutdown() {
	sm.runtimeDefaultsOnce.Do(sm.ensureRuntimeDefaults)

	sm.shutdownOnce.Do(func() {
		// Mark shutdown as initiated under the lifecycle lock: any server
		// launch that has not published its lifecycle signal yet is refused
		// from this point on, and any signal published before is visible here.
		sm.lifecycleMu.Lock()
		sm.shuttingDown = true
		sm.lifecycleMu.Unlock()

		// Use a non-blocking read to check if servers have started.
		// This prevents a deadlock if a panic occurs before startServers() completes.
		select {
		case <-sm.serversStarted:
			// Servers started, proceed with normal shutdown.
		default:
			// Servers did not start (or start was interrupted).
			sm.logInfo("Shutdown initiated before servers were fully started.")
		}

		// The additional and main HTTP servers drain concurrently under ONE
		// shutdownTimeout budget, created once before either starts: a stuck
		// request on one surface neither delays the other's drain nor doubles
		// the total. gRPC and admin follow, each with its own budget.
		drainCtx, cancelDrain := context.WithTimeout(context.Background(), sm.shutdownTimeout)
		additionalDrained := sm.drainAdditionalHTTPServer(drainCtx)

		sm.shutdownHTTPServer(drainCtx)
		<-additionalDrained
		cancelDrain()

		// Shutdown the gRPC server BEFORE telemetry to allow in-flight RPCs
		// to complete and emit their final spans/metrics before the telemetry
		// pipeline is torn down.
		if sm.grpcServer != nil {
			sm.logInfo("Shutting down gRPC server...")

			done := make(chan struct{})

			runtime.SafeGoWithContextAndComponent(
				context.Background(),
				sm.logger,
				"server",
				"grpc_graceful_stop",
				runtime.KeepRunning,
				func(_ context.Context) {
					sm.grpcServer.GracefulStop()
					close(done)
				},
			)

			select {
			case <-done:
				sm.logInfo("gRPC server stopped gracefully")
			case <-time.After(sm.shutdownTimeout):
				sm.logInfo("gRPC graceful stop timed out, forcing stop...")
				sm.grpcServer.Stop()
			}
		}

		// The admin server drains LAST: readiness and health probes keep
		// being answered while the API and gRPC servers finish their
		// in-flight work, so an orchestrator sees the pod leave rotation
		// before it stops answering at all.
		sm.shutdownAdminHTTPServer()

		// Execute shutdown hooks (best-effort) after HTTP and gRPC servers have
		// drained/stopped, but before telemetry/logger/license shutdown. Each hook
		// gets its own context with an independent timeout to prevent one slow hook
		// from consuming the entire budget.
		for i, hook := range sm.shutdownHooks {
			func(i int, hook func(context.Context) error) {
				hookCtx, hookCancel := context.WithTimeout(context.Background(), sm.shutdownTimeout)
				defer hookCancel()
				defer func() {
					if r := recover(); r != nil {
						sm.logger.Log(context.Background(), obs.LevelError, "shutdown hook panicked",
							"hook_index", i,
							"panic", r,
						)
						runtime.HandlePanicValue(context.Background(), sm.logger, r, "server", "shutdown_hook")
					}
				}()

				if err := hook(hookCtx); err != nil {
					sm.logger.Log(context.Background(), obs.LevelError, "shutdown hook failed",
						"hook_index", i,
						"error", err,
					)
				}
			}(i, hook)
		}

		// Shutdown telemetry AFTER servers have drained, so final spans/metrics are exported.
		if !nilcheck.Interface(sm.telemetry) {
			sm.logInfo("Shutting down telemetry...")
			sm.telemetry.ShutdownTelemetry()
		}

		// Sync logger if available
		if !nilcheck.Interface(sm.logger) {
			sm.logInfo("Syncing logger...")

			if err := sm.logger.Sync(context.Background()); err != nil {
				sm.logger.Log(context.Background(), obs.LevelError, "failed to sync logger", "error", err)
			}
		}

		// License termination handlers are for validation failures only. Invoking
		// them during normal graceful shutdown can turn a clean SIGTERM into exit 1.
		if sm.licenseClient != nil {
			sm.logInfo("Skipping license termination handler during graceful shutdown")
		}

		sm.logInfo("Graceful shutdown completed")
	})
}

// drainAdditionalHTTPServer drains the additional stdlib server in its own
// goroutine under ctx. The returned channel closes when the drain is over,
// immediately when no additional server is configured.
func (sm *ServerManager) drainAdditionalHTTPServer(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})

	if sm.additionalHTTP == nil {
		close(done)

		return done
	}

	runtime.SafeGoWithContextAndComponent(
		context.Background(),
		sm.logger,
		"server",
		"additional_stdlib_http_shutdown",
		runtime.KeepRunning,
		func(_ context.Context) {
			defer close(done)

			sm.shutdownStdlibServer(ctx, sm.additionalHTTP, "additional HTTP")
		},
	)

	return done
}

func (sm *ServerManager) shutdownHTTPServer(ctx context.Context) {
	// Shutdown the HTTP server if available. The fiber and stdlib paths
	// are mutually exclusive (enforced by validateConfiguration), so at
	// most one of these branches fires per shutdown.
	switch {
	case sm.httpServer != nil:
		sm.logInfo("Shutting down HTTP server...")

		if err := sm.httpServer.Shutdown(); err != nil {
			sm.logger.Log(context.Background(), obs.LevelError, "error during HTTP server shutdown", "error", err)
		}

		sm.awaitFiberListenExit(sm.httpServer, &sm.fiberListenDone)
	case sm.stdlibHTTPServer != nil:
		sm.shutdownStdlibServer(ctx, sm.stdlibHTTPServer, "HTTP")
	}
}

// shutdownAdminHTTPServer drains the admin app. Called last in the shutdown
// sequence so the operational endpoints outlive the traffic-serving ones.
func (sm *ServerManager) shutdownAdminHTTPServer() {
	if sm.adminServer == nil {
		return
	}

	sm.logInfo("Shutting down admin HTTP server...")

	if err := sm.adminServer.Shutdown(); err != nil {
		sm.logger.Log(context.Background(), obs.LevelError, "error during admin HTTP server shutdown", "error", err)
	}

	sm.awaitFiberListenExit(sm.adminServer, &sm.adminListenDone)
}

// awaitFiberListenExit blocks until the fiber Listen goroutine has returned,
// re-issuing Shutdown while it waits. fiber's Shutdown is a silent no-op when
// it runs before Listen has registered its listener with the underlying
// fasthttp server (registration happens inside Serve), so a shutdown that
// races server startup would otherwise leave the Listen goroutine serving
// forever. That leaked goroutine outlives shutdown and, under the race
// detector, trips on process globals such as os.Stdout that fiber's startup
// path reads and the test harness swaps between tests and examples.
func (sm *ServerManager) awaitFiberListenExit(app *fiber.App, listenDone *chan struct{}) {
	sm.lifecycleMu.Lock()
	done := *listenDone
	sm.lifecycleMu.Unlock()

	// Nil means the fiber launch goroutine was never spawned (and, with
	// shuttingDown now set, never will be), so there is nothing to wait for.
	if done == nil {
		return
	}

	timeout := sm.shutdownTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	retry := time.NewTicker(10 * time.Millisecond)
	defer retry.Stop()

	for {
		select {
		case <-done:
			return
		case <-deadline.C:
			sm.logInfo("Timed out waiting for the HTTP listen goroutine to exit")
			return
		case <-retry.C:
			// Listen may not have reached Serve when the first Shutdown ran;
			// re-issue it so the bound listener is closed once registered.
			_ = app.Shutdown()
		}
	}
}

// shutdownStdlibServer drains a stdlib server under ctx; label names it in
// logs. ctx is the drain budget, derived from context.Background() bounded by
// shutdownTimeout, so an earlier signal/ctx cancellation does not abort the
// in-flight request drain. If the drain fails or times out, fall back to
// Close() so active connections are forcefully released instead of leaking
// past the shutdown budget.
func (sm *ServerManager) shutdownStdlibServer(ctx context.Context, srv *http.Server, label string) {
	sm.logInfo("Shutting down " + label + " server...")

	if err := srv.Shutdown(ctx); err != nil {
		sm.logger.Log(context.Background(), obs.LevelError, "error during "+label+" server shutdown", "error", err)

		if closeErr := srv.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
			sm.logger.Log(context.Background(), obs.LevelError, "error during "+label+" server hard close", "error", closeErr)
		}
	}
}
