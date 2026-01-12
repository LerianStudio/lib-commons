package server

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/LerianStudio/lib-commons/v2/commons/license"
	"github.com/LerianStudio/lib-commons/v2/commons/log"
	"github.com/LerianStudio/lib-commons/v2/commons/opentelemetry"
	"github.com/gofiber/fiber/v2"
	"google.golang.org/grpc"
)

// ErrNoServersConfigured indicates no servers were configured for the manager
var ErrNoServersConfigured = errors.New("no servers configured: use WithHTTPServer() or WithGRPCServer()")

// ServerManager handles the graceful shutdown of multiple server types.
// It can manage HTTP servers, gRPC servers, or both simultaneously.
type ServerManager struct {
	httpServer     *fiber.App
	grpcServer     *grpc.Server
	licenseClient  *license.ManagerShutdown
	telemetry      *opentelemetry.Telemetry
	logger         log.Logger
	httpAddress    string
	grpcAddress    string
	serversStarted chan struct{}
	shutdownChan   <-chan struct{}
}

// NewServerManager creates a new instance of ServerManager.
func NewServerManager(
	licenseClient *license.ManagerShutdown,
	telemetry *opentelemetry.Telemetry,
	logger log.Logger,
) *ServerManager {
	return &ServerManager{
		licenseClient:  licenseClient,
		telemetry:      telemetry,
		logger:         logger,
		serversStarted: make(chan struct{}),
	}
}

// WithHTTPServer configures the HTTP server for the ServerManager.
func (sm *ServerManager) WithHTTPServer(app *fiber.App, address string) *ServerManager {
	sm.httpServer = app
	sm.httpAddress = address

	return sm
}

// WithGRPCServer configures the gRPC server for the ServerManager.
func (sm *ServerManager) WithGRPCServer(server *grpc.Server, address string) *ServerManager {
	sm.grpcServer = server
	sm.grpcAddress = address

	return sm
}

// WithShutdownChannel configures a custom shutdown channel for the ServerManager.
// This allows tests to trigger shutdown deterministically instead of relying on OS signals.
func (sm *ServerManager) WithShutdownChannel(ch <-chan struct{}) *ServerManager {
	sm.shutdownChan = ch

	return sm
}

func (sm *ServerManager) validateConfiguration() error {
	if sm.httpServer == nil && sm.grpcServer == nil {
		return ErrNoServersConfigured
	}

	return nil
}

// initServers validates configuration and starts servers without blocking.
// Returns an error if validation fails. Does not call Fatal.
func (sm *ServerManager) initServers() error {
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
	if err := sm.initServers(); err != nil {
		return err
	}

	sm.handleShutdown()

	return nil
}

// StartWithGracefulShutdown initializes all configured servers and sets up graceful shutdown.
// It will call Fatal if no servers are configured (backward compatible behavior).
// Use StartWithGracefulShutdownWithError() for proper error handling instead.
func (sm *ServerManager) StartWithGracefulShutdown() {
	if err := sm.initServers(); err != nil {
		sm.logFatal(err.Error())
		return
	}

	// Run everything in a recover block
	defer func() {
		if r := recover(); r != nil {
			if sm.logger != nil {
				sm.logger.Errorf("Fatal error (panic): %v", r)
			} else {
				fmt.Printf("Fatal error (panic): %v\n", r)
			}

			sm.executeShutdown()

			os.Exit(1)
		}
	}()

	sm.handleShutdown()
}

// startServers starts all configured servers in separate goroutines.
// Note: Validation is performed by validateConfiguration() before this method is called.
// Callers using StartWithGracefulShutdown() directly will still get Fatal behavior for backward compatibility,
// while StartWithGracefulShutdownWithError() validates first and returns an error.
func (sm *ServerManager) startServers() {
	started := 0

	// Start HTTP server if configured
	if sm.httpServer != nil {
		go func() {
			sm.logInfof("Starting HTTP server on %s", sm.httpAddress)

			if err := sm.httpServer.Listen(sm.httpAddress); err != nil {
				sm.logErrorf("HTTP server error: %v", err)
			}
		}()

		started++
	}

	// Start gRPC server if configured
	if sm.grpcServer != nil {
		go func() {
			sm.logInfof("Starting gRPC server on %s", sm.grpcAddress)

			listener, err := net.Listen("tcp", sm.grpcAddress)
			if err != nil {
				sm.logErrorf("Failed to listen on gRPC address: %v", err)
				return
			}

			if err := sm.grpcServer.Serve(listener); err != nil {
				sm.logErrorf("gRPC server error: %v", err)
			}
		}()

		started++
	}

	sm.logInfof("Started %d server(s)", started)

	close(sm.serversStarted)
}

// logInfo safely logs an info message if logger is available
func (sm *ServerManager) logInfo(msg string) {
	if sm.logger != nil {
		sm.logger.Info(msg)
	}
}

// logInfof safely logs a formatted info message if logger is available
func (sm *ServerManager) logInfof(format string, args ...any) {
	if sm.logger != nil {
		sm.logger.Infof(format, args...)
	}
}

// logErrorf safely logs an error message if logger is available
func (sm *ServerManager) logErrorf(format string, args ...any) {
	if sm.logger != nil {
		sm.logger.Errorf(format, args...)
	}
}

// logFatal logs a fatal message and terminates the process.
// If logger is available, uses logger.Fatal (which calls os.Exit(1) internally).
// If logger is nil, prints to stdout and calls os.Exit(1) explicitly.
// This ensures consistent termination behavior regardless of logger availability.
func (sm *ServerManager) logFatal(msg string) {
	if sm.logger != nil {
		sm.logger.Fatal(msg)
	} else {
		fmt.Println(msg)
		os.Exit(1)
	}
}

// handleShutdown sets up signal handling and executes the shutdown sequence
// when a termination signal is received or when the shutdown channel is closed.
func (sm *ServerManager) handleShutdown() {
	if sm.shutdownChan != nil {
		<-sm.shutdownChan
	} else {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		<-c
	}

	sm.logInfo("Gracefully shutting down all servers...")

	sm.executeShutdown()
}

// executeShutdown performs the actual shutdown operations in the correct order for ServerManager.
func (sm *ServerManager) executeShutdown() {
	// Use a non-blocking read to check if servers have started.
	// This prevents a deadlock if a panic occurs before startServers() completes.
	select {
	case <-sm.serversStarted:
		// Servers started, proceed with normal shutdown.
	default:
		// Servers did not start (or start was interrupted).
		sm.logInfo("Shutdown initiated before servers were fully started.")
	}

	// Shutdown the HTTP server if available
	if sm.httpServer != nil {
		sm.logInfo("Shutting down HTTP server...")

		if err := sm.httpServer.Shutdown(); err != nil {
			sm.logErrorf("Error during HTTP server shutdown: %v", err)
		}
	}

	// Shutdown telemetry BEFORE gRPC server to allow metrics export
	if sm.telemetry != nil {
		sm.logInfo("Shutting down telemetry...")
		sm.telemetry.ShutdownTelemetry()
	}

	// Shutdown the gRPC server if available
	if sm.grpcServer != nil {
		sm.logInfo("Shutting down gRPC server...")

		// Use GracefulStop which waits for all RPCs to finish
		sm.grpcServer.GracefulStop()
		sm.logInfo("gRPC server stopped gracefully")
	}

	// Sync logger if available
	if sm.logger != nil {
		sm.logInfo("Syncing logger...")

		if err := sm.logger.Sync(); err != nil {
			sm.logErrorf("Failed to sync logger: %v", err)
		}
	}

	// Shutdown license background refresh if available
	if sm.licenseClient != nil {
		sm.logInfo("Shutting down license background refresh...")
		sm.licenseClient.Terminate("shutdown")
	}

	sm.logInfo("Graceful shutdown completed")
}

// GracefulShutdown handles the graceful shutdown of application components.
// It's designed to be reusable across different services.
// Deprecated: Use ServerManager instead for better coordination.
type GracefulShutdown struct {
	app           *fiber.App
	grpcServer    *grpc.Server
	licenseClient *license.ManagerShutdown
	telemetry     *opentelemetry.Telemetry
	logger        log.Logger
}

// NewGracefulShutdown creates a new instance of GracefulShutdown.
// Deprecated: Use NewServerManager instead for better coordination.
func NewGracefulShutdown(
	app *fiber.App,
	grpcServer *grpc.Server,
	licenseClient *license.ManagerShutdown,
	telemetry *opentelemetry.Telemetry,
	logger log.Logger,
) *GracefulShutdown {
	return &GracefulShutdown{
		app:           app,
		grpcServer:    grpcServer,
		licenseClient: licenseClient,
		telemetry:     telemetry,
		logger:        logger,
	}
}

// HandleShutdown sets up signal handling and executes the shutdown sequence
// when a termination signal is received.
// Deprecated: Use ServerManager.StartWithGracefulShutdown() instead.
func (gs *GracefulShutdown) HandleShutdown() {
	// Create channel for shutdown signals
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	// Block until we receive a signal
	<-c
	gs.logger.Info("Gracefully shutting down...")

	// Execute shutdown sequence
	gs.executeShutdown()
}

// executeShutdown performs the actual shutdown operations in the correct order.
// Deprecated: Use ServerManager.executeShutdown() for better coordination.
func (gs *GracefulShutdown) executeShutdown() {
	// Shutdown the HTTP server if available
	if gs.app != nil {
		gs.logger.Info("Shutting down HTTP server...")

		if err := gs.app.Shutdown(); err != nil {
			gs.logger.Errorf("Error during HTTP server shutdown: %v", err)
		}
	}

	// Shutdown the gRPC server if available
	if gs.grpcServer != nil {
		gs.logger.Info("Shutting down gRPC server...")

		// Use GracefulStop which waits for all RPCs to finish
		gs.grpcServer.GracefulStop()
		gs.logger.Info("gRPC server stopped gracefully")
	}

	// Shutdown telemetry if available
	if gs.telemetry != nil {
		gs.logger.Info("Shutting down telemetry...")
		gs.telemetry.ShutdownTelemetry()
	}

	// Sync logger if available
	if gs.logger != nil {
		gs.logger.Info("Syncing logger...")

		if err := gs.logger.Sync(); err != nil {
			gs.logger.Errorf("Failed to sync logger: %v", err)
		}
	}

	// Shutdown license background refresh if available
	if gs.licenseClient != nil {
		gs.logger.Info("Shutting down license background refresh...")
		gs.licenseClient.Terminate("shutdown")
	}

	gs.logger.Info("Graceful shutdown completed")
}
