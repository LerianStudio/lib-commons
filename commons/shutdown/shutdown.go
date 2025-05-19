package shutdown

import (
	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/LerianStudio/lib-commons/commons/opentelemetry"
	libLicense "github.com/LerianStudio/lib-license-go/middleware"
	"github.com/gofiber/fiber/v2"
	"os"
	"os/signal"
	"syscall"
)

// GracefulShutdown handles the graceful shutdown of application components.
// It's designed to be reusable across different services.
type GracefulShutdown struct {
	app           *fiber.App
	licenseClient *libLicense.LicenseClient
	telemetry     *opentelemetry.Telemetry
	logger        log.Logger
}

// NewGracefulShutdown creates a new instance of GracefulShutdown.
func NewGracefulShutdown(
	app *fiber.App,
	licenseClient *libLicense.LicenseClient,
	telemetry *opentelemetry.Telemetry,
	logger log.Logger,
) *GracefulShutdown {
	return &GracefulShutdown{
		app:           app,
		licenseClient: licenseClient,
		telemetry:     telemetry,
		logger:        logger,
	}
}

// HandleShutdown sets up signal handling and executes the shutdown sequence
// when a termination signal is received.
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
func (gs *GracefulShutdown) executeShutdown() {
	// Shutdown the server
	if gs.app != nil {
		gs.logger.Info("Shutting down HTTP server...")
		if err := gs.app.Shutdown(); err != nil {
			gs.logger.Errorf("Error during server shutdown: %v", err)
		}
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
		gs.licenseClient.ShutdownBackgroundRefresh()
	}

	gs.logger.Info("Graceful shutdown completed")
}

// StartServerWithGracefulShutdown starts the Fiber server in a goroutine and
// sets up graceful shutdown handling.
func StartServerWithGracefulShutdown(
	app *fiber.App,
	licenseClient *libLicense.LicenseClient,
	telemetry *opentelemetry.Telemetry,
	serverAddress string,
	logger log.Logger,
) {
	// Initialize telemetry if available
	if telemetry != nil {
		telemetry.InitializeTelemetry(logger)
	}

	// Create graceful shutdown handler
	gs := NewGracefulShutdown(app, licenseClient, telemetry, logger)

	// Run everything in a recover block
	defer func() {
		if r := recover(); r != nil {
			logger.Errorf("Fatal error (panic): %v", r)
			gs.executeShutdown()
			os.Exit(1)
		}
	}()

	// Start server in a separate goroutine
	go func() {
		logger.Infof("Starting HTTP server on %s", serverAddress)
		if err := app.Listen(serverAddress); err != nil {
			// During normal shutdown, app.Listen() will return an error
			// We only want to log unexpected errors
			logger.Errorf("Server error: %v", err)
		}
	}()

	// Handle graceful shutdown
	gs.HandleShutdown()
}
