// Package server provides examples of how to use the unified ServerManager
// for different server configurations.
package server

import (
	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/LerianStudio/lib-commons/commons/opentelemetry"
	"github.com/gofiber/fiber/v2"
	"google.golang.org/grpc"
)

// ExampleHTTPOnly demonstrates how to use ServerManager with only an HTTP server.
func ExampleHTTPOnly() {
	// Create your HTTP server
	app := fiber.New()

	// Setup your routes
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "healthy"})
	})

	// Create logger, telemetry, and license client
	logger := &log.GoLogger{}                  // Replace with your logger initialization
	telemetry := &opentelemetry.Telemetry{}    // Replace with your telemetry initialization
	licenseClient := &LicenseManagerShutdown{} // Replace with your license client

	// Create ServerManager and configure HTTP server
	serverManager := NewServerManager(licenseClient, telemetry, logger).
		WithHTTPServer(app, ":8080")

	// Start the server with graceful shutdown
	// This will block until a shutdown signal is received
	serverManager.StartWithGracefulShutdown()
}

// ExampleGRPCOnly demonstrates how to use ServerManager with only a gRPC server.
func ExampleGRPCOnly() {
	// Create your gRPC server
	grpcServer := grpc.NewServer()

	// Register your gRPC services here
	// pb.RegisterYourServiceServer(grpcServer, &yourServiceImpl{})

	// Create logger, telemetry, and license client
	logger := &log.GoLogger{}                  // Replace with your logger initialization
	telemetry := &opentelemetry.Telemetry{}    // Replace with your telemetry initialization
	licenseClient := &LicenseManagerShutdown{} // Replace with your license client

	// Create ServerManager and configure gRPC server
	serverManager := NewServerManager(licenseClient, telemetry, logger).
		WithGRPCServer(grpcServer, ":50051")

	// Start the server with graceful shutdown
	// This will block until a shutdown signal is received
	serverManager.StartWithGracefulShutdown()
}

// ExampleBothServers demonstrates how to use ServerManager with both HTTP and gRPC servers.
// This is the main advantage of the unified approach - both servers are coordinated properly.
func ExampleBothServers() {
	// Create your HTTP server
	app := fiber.New()
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "healthy"})
	})

	// Create your gRPC server
	grpcServer := grpc.NewServer()
	// Register your gRPC services here
	// pb.RegisterYourServiceServer(grpcServer, &yourServiceImpl{})

	// Create logger, telemetry, and license client
	logger := &log.GoLogger{}                  // Replace with your logger initialization
	telemetry := &opentelemetry.Telemetry{}    // Replace with your telemetry initialization
	licenseClient := &LicenseManagerShutdown{} // Replace with your license client

	// Create ServerManager and configure both servers
	serverManager := NewServerManager(licenseClient, telemetry, logger).
		WithHTTPServer(app, ":8080").
		WithGRPCServer(grpcServer, ":50051")

	// Start both servers with coordinated graceful shutdown
	// This will block until a shutdown signal is received
	// Both servers will be shut down gracefully in the correct order
	serverManager.StartWithGracefulShutdown()
}

// ExampleMigrationFromOldAPI demonstrates how to migrate from the old API to the new unified API.
func ExampleMigrationFromOldAPI() {
	// OLD WAY (problematic with both servers):
	//
	// go StartHTTPServerWithGracefulShutdown(app, licenseClient, telemetry, ":8080", logger)
	// StartGRPCServerWithGracefulShutdown(grpcServer, licenseClient, telemetry, ":50051", logger)
	//
	// Issues with old way:
	// - Duplicate signal handling
	// - Duplicate telemetry/license initialization
	// - No coordination between servers
	// - If one server panics, the other doesn't clean up properly
	// NEW WAY (unified and coordinated):
	app := fiber.New()
	grpcServer := grpc.NewServer()
	logger := &log.GoLogger{}
	telemetry := &opentelemetry.Telemetry{}
	licenseClient := &LicenseManagerShutdown{}

	NewServerManager(licenseClient, telemetry, logger).
		WithHTTPServer(app, ":8080").
		WithGRPCServer(grpcServer, ":50051").
		StartWithGracefulShutdown()

	// Benefits of new way:
	// - Single signal handler for all servers
	// - Single telemetry/license initialization
	// - Coordinated shutdown of all servers
	// - Proper cleanup even if one server fails
	// - Clean, fluent API
}
