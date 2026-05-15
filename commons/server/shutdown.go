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

	"github.com/LerianStudio/lib-commons/v5/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-commons/v5/commons/license"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/runtime"
	"github.com/gofiber/fiber/v2"
	"google.golang.org/grpc"
)

// ErrNoServersConfigured indicates no servers were configured for the manager.
var ErrNoServersConfigured = errors.New("no servers configured: use WithHTTPServer(), WithStdlibHTTPServer(), or WithGRPCServer()")

// ErrConflictingHTTPServers indicates that both WithHTTPServer(*fiber.App) and
// WithStdlibHTTPServer(*http.Server) were configured on the same ServerManager.
// These two configurators are mutually exclusive: pick exactly one HTTP path.
var ErrConflictingHTTPServers = errors.New("conflicting HTTP servers configured: WithHTTPServer(*fiber.App) and WithStdlibHTTPServer(*http.Server) are mutually exclusive")

const defaultReadHeaderTimeout = 5 * time.Second

// ServerManager handles the graceful shutdown of multiple server types.
// It can manage HTTP servers (either *fiber.App or stdlib *http.Server, but
// not both), gRPC servers, or any compatible combination simultaneously.
type ServerManager struct {
	httpServer         *fiber.App
	stdlibHTTPServer   *http.Server
	grpcServer         *grpc.Server
	stdlibHTTPListener net.Listener
	licenseClient      *license.ManagerShutdown
	telemetry          *opentelemetry.Telemetry
	logger             log.Logger
	httpAddress        string
	grpcAddress        string
	serversStarted     chan struct{}
	serversStartedOnce sync.Once
	shutdownChan       <-chan struct{}
	shutdownOnce       sync.Once
	shutdownTimeout    time.Duration
	startupErrors      chan error
	shutdownHooks      []func(context.Context) error
}

// ensureRuntimeDefaults initializes zero-value fields so exported lifecycle
// methods remain nil-safe even when ServerManager is manually instantiated.
func (sm *ServerManager) ensureRuntimeDefaults() {
	if sm == nil {
		return
	}

	if nilcheck.Interface(sm.logger) {
		sm.logger = log.NewNop()
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
func NewServerManager(
	licenseClient *license.ManagerShutdown,
	telemetry *opentelemetry.Telemetry,
	logger log.Logger,
) *ServerManager {
	if nilcheck.Interface(logger) {
		logger = log.NewNop()
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

	sm.ensureRuntimeDefaults()

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

	if sm.httpServer == nil && sm.stdlibHTTPServer == nil && sm.grpcServer == nil {
		return ErrNoServersConfigured
	}

	return nil
}

// initServers validates configuration and starts servers without blocking.
// Returns an error if validation fails. Does not call Fatal.
func (sm *ServerManager) initServers() error {
	sm.ensureRuntimeDefaults()

	if err := sm.validateConfiguration(); err != nil {
		return err
	}

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

	sm.ensureRuntimeDefaults()

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
		fmt.Println("no servers configured: use WithHTTPServer(), WithStdlibHTTPServer(), or WithGRPCServer()")
		os.Exit(1)
	}

	sm.ensureRuntimeDefaults()

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
// Launch order is fiber HTTP → stdlib HTTP → gRPC. The two HTTP branches are
// mutually exclusive (enforced by validateConfiguration) so at most one of
// them fires; the gRPC branch is independent and composes with either.
func (sm *ServerManager) startServers() {
	started := 0

	if sm.launchFiberHTTPServer() {
		started++
	}

	if sm.launchStdlibHTTPServer() {
		started++
	}

	if sm.launchGRPCServer() {
		started++
	}

	sm.logger.Log(context.Background(), log.LevelInfo, "launched server goroutines", log.Int("count", started))

	// Signal that server goroutines have been launched (not that sockets are bound).
	sm.serversStartedOnce.Do(func() {
		close(sm.serversStarted)
	})
}

// launchFiberHTTPServer spawns the fiber HTTP launch goroutine. Returns true
// if a goroutine was launched, false if no fiber server is configured.
func (sm *ServerManager) launchFiberHTTPServer() bool {
	if sm.httpServer == nil {
		return false
	}

	runtime.SafeGoWithContextAndComponent(
		context.Background(),
		sm.logger,
		"server",
		"start_http_server",
		runtime.KeepRunning,
		func(_ context.Context) {
			sm.logger.Log(context.Background(), log.LevelInfo, "starting HTTP server", log.String("address", sm.httpAddress))

			if err := sm.httpServer.Listen(sm.httpAddress); err != nil {
				sm.logger.Log(context.Background(), log.LevelError, "HTTP server error", log.Err(err))

				select {
				case sm.startupErrors <- fmt.Errorf("HTTP server: %w", err):
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
	if sm.stdlibHTTPServer == nil {
		return false
	}

	runtime.SafeGoWithContextAndComponent(
		context.Background(),
		sm.logger,
		"server",
		"start_stdlib_http_server",
		runtime.KeepRunning,
		func(_ context.Context) {
			address := sm.stdlibHTTPServer.Addr
			if sm.stdlibHTTPListener != nil {
				address = sm.stdlibHTTPListener.Addr().String()
			}

			sm.logger.Log(context.Background(), log.LevelInfo, "starting stdlib HTTP server", log.String("address", address))

			// ListenAndServe returns http.ErrServerClosed on a clean
			// Shutdown — that is the success signal, not an error.
			// Parity with fiber.App.Listen returning nil after
			// fiber.App.Shutdown.
			var err error
			if sm.stdlibHTTPListener != nil {
				err = sm.stdlibHTTPServer.Serve(sm.stdlibHTTPListener)
			} else {
				err = sm.stdlibHTTPServer.ListenAndServe()
			}

			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				sm.logger.Log(context.Background(), log.LevelError, "stdlib HTTP server error", log.Err(err))

				select {
				case sm.startupErrors <- fmt.Errorf("HTTP server: %w", err):
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
			sm.logger.Log(context.Background(), log.LevelInfo, "starting gRPC server", log.String("address", sm.grpcAddress))

			listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", sm.grpcAddress)
			if err != nil {
				sm.logger.Log(context.Background(), log.LevelError, "failed to listen on gRPC address", log.Err(err))

				select {
				case sm.startupErrors <- fmt.Errorf("gRPC listen: %w", err):
				default:
				}

				return
			}

			if err := sm.grpcServer.Serve(listener); err != nil {
				sm.logger.Log(context.Background(), log.LevelError, "gRPC server error", log.Err(err))

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
		sm.logger.Log(context.Background(), log.LevelInfo, msg)
	}
}

// logFatal logs a fatal message and terminates the process with os.Exit(1).
// Uses Error level for logging to avoid relying on logger implementations
// that may or may not call os.Exit(1) in their Fatal method.
func (sm *ServerManager) logFatal(msg string) {
	if !nilcheck.Interface(sm.logger) {
		sm.logger.Log(context.Background(), log.LevelError, msg)
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
	sm.ensureRuntimeDefaults()

	var startupErr error

	if sm.shutdownChan != nil {
		select {
		case <-sm.shutdownChan:
		case err := <-sm.startupErrors:
			sm.logger.Log(context.Background(), log.LevelError, "server startup failed", log.Err(err))

			startupErr = err
		}
	} else {
		c := make(chan os.Signal, 1)

		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(c)

		select {
		case <-c:
		case err := <-sm.startupErrors:
			sm.logger.Log(context.Background(), log.LevelError, "server startup failed", log.Err(err))

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
	sm.ensureRuntimeDefaults()

	sm.shutdownOnce.Do(func() {
		// Use a non-blocking read to check if servers have started.
		// This prevents a deadlock if a panic occurs before startServers() completes.
		select {
		case <-sm.serversStarted:
			// Servers started, proceed with normal shutdown.
		default:
			// Servers did not start (or start was interrupted).
			sm.logInfo("Shutdown initiated before servers were fully started.")
		}

		sm.shutdownHTTPServer()

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

		// Execute shutdown hooks (best-effort) after HTTP and gRPC servers have
		// drained/stopped, but before telemetry/logger/license shutdown. Each hook
		// gets its own context with an independent timeout to prevent one slow hook
		// from consuming the entire budget.
		for i, hook := range sm.shutdownHooks {
			hookCtx, hookCancel := context.WithTimeout(context.Background(), sm.shutdownTimeout)

			if err := hook(hookCtx); err != nil {
				sm.logger.Log(context.Background(), log.LevelError, "shutdown hook failed",
					log.Int("hook_index", i),
					log.Err(err),
				)
			}

			hookCancel()
		}

		// Shutdown telemetry AFTER servers have drained, so final spans/metrics are exported.
		if sm.telemetry != nil {
			sm.logInfo("Shutting down telemetry...")
			sm.telemetry.ShutdownTelemetry()
		}

		// Sync logger if available
		if !nilcheck.Interface(sm.logger) {
			sm.logInfo("Syncing logger...")

			if err := sm.logger.Sync(context.Background()); err != nil {
				sm.logger.Log(context.Background(), log.LevelError, "failed to sync logger", log.Err(err))
			}
		}

		// Shutdown license background refresh if available
		if sm.licenseClient != nil {
			sm.logInfo("Shutting down license background refresh...")
			sm.licenseClient.Terminate("shutdown")
		}

		sm.logInfo("Graceful shutdown completed")
	})
}

func (sm *ServerManager) shutdownHTTPServer() {
	// Shutdown the HTTP server if available. The fiber and stdlib paths
	// are mutually exclusive (enforced by validateConfiguration), so at
	// most one of these branches fires per shutdown.
	switch {
	case sm.httpServer != nil:
		sm.logInfo("Shutting down HTTP server...")

		if err := sm.httpServer.Shutdown(); err != nil {
			sm.logger.Log(context.Background(), log.LevelError, "error during HTTP server shutdown", log.Err(err))
		}
	case sm.stdlibHTTPServer != nil:
		sm.shutdownStdlibHTTPServer()
	}
}

func (sm *ServerManager) shutdownStdlibHTTPServer() {
	sm.logInfo("Shutting down HTTP server...")

	// Bound the drain by shutdownTimeout. Unlike fiber.App.Shutdown,
	// stdlib http.Server.Shutdown accepts an explicit ctx — we use a
	// fresh background-derived ctx so an earlier signal/ctx
	// cancellation does not abort the in-flight request drain. If the
	// drain fails or times out, fall back to Close() so active
	// connections are forcefully released instead of leaking past the
	// shutdown budget.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), sm.shutdownTimeout)
	if err := sm.stdlibHTTPServer.Shutdown(shutdownCtx); err != nil {
		sm.logger.Log(context.Background(), log.LevelError, "error during HTTP server shutdown", log.Err(err))

		if closeErr := sm.stdlibHTTPServer.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
			sm.logger.Log(context.Background(), log.LevelError, "error during HTTP server hard close", log.Err(closeErr))
		}
	}

	cancel()
}
