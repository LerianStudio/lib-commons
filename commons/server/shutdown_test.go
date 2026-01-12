package server_test

import (
	"errors"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v2/commons/server"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
)

func TestNewGracefulShutdown(t *testing.T) {
	gs := server.NewGracefulShutdown(nil, nil, nil, nil, nil)
	assert.NotNil(t, gs, "NewGracefulShutdown should return a non-nil instance")
}

func TestNewGracefulShutdownWithGRPC(t *testing.T) {
	gs := server.NewGracefulShutdown(nil, nil, nil, nil, nil)
	assert.NotNil(t, gs, "NewGracefulShutdown should return a non-nil instance with gRPC server")
}

func TestNewServerManager(t *testing.T) {
	sm := server.NewServerManager(nil, nil, nil)
	assert.NotNil(t, sm, "NewServerManager should return a non-nil instance")
}

func TestServerManagerWithHTTPOnly(t *testing.T) {
	app := fiber.New()
	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(app, ":8080")
	assert.NotNil(t, sm, "ServerManager with HTTP server should return a non-nil instance")
}

func TestServerManagerWithGRPCOnly(t *testing.T) {
	grpcServer := grpc.NewServer()
	sm := server.NewServerManager(nil, nil, nil).
		WithGRPCServer(grpcServer, ":50051")
	assert.NotNil(t, sm, "ServerManager with gRPC server should return a non-nil instance")
}

func TestServerManagerWithBothServers(t *testing.T) {
	app := fiber.New()
	grpcServer := grpc.NewServer()
	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(app, ":8080").
		WithGRPCServer(grpcServer, ":50051")
	assert.NotNil(t, sm, "ServerManager with both servers should return a non-nil instance")
}

func TestServerManagerChaining(t *testing.T) {
	app := fiber.New()
	grpcServer := grpc.NewServer()

	// Test method chaining
	sm1 := server.NewServerManager(nil, nil, nil).WithHTTPServer(app, ":8080")
	sm2 := sm1.WithGRPCServer(grpcServer, ":50051")

	assert.Equal(t, sm1, sm2, "Method chaining should return the same instance")
}

func TestStartWithGracefulShutdownWithError_NoServers(t *testing.T) {
	sm := server.NewServerManager(nil, nil, nil)

	err := sm.StartWithGracefulShutdownWithError()

	assert.Error(t, err)
	assert.True(t, errors.Is(err, server.ErrNoServersConfigured))
}

func TestErrNoServersConfigured(t *testing.T) {
	assert.NotNil(t, server.ErrNoServersConfigured)
	assert.Contains(t, server.ErrNoServersConfigured.Error(), "no servers configured")
}

func TestStartWithGracefulShutdownWithError_HTTPServer_Success(t *testing.T) {
	app := fiber.New()
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(app, ":0").
		WithShutdownChannel(shutdownChan)

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	close(shutdownChan)

	select {
	case err := <-done:
		assert.NoError(t, err, "StartWithGracefulShutdownWithError should complete without error")
	case <-time.After(5 * time.Second):
		t.Fatal("Test timed out waiting for StartWithGracefulShutdownWithError to complete")
	}
}

func TestStartWithGracefulShutdownWithError_GRPCServer_Success(t *testing.T) {
	grpcServer := grpc.NewServer()
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithGRPCServer(grpcServer, ":0").
		WithShutdownChannel(shutdownChan)

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	close(shutdownChan)

	err := <-done
	assert.NoError(t, err, "StartWithGracefulShutdownWithError should complete without error")
}

func TestStartWithGracefulShutdownWithError_BothServers_Success(t *testing.T) {
	app := fiber.New()
	grpcServer := grpc.NewServer()
	shutdownChan := make(chan struct{})

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServer(app, ":0").
		WithGRPCServer(grpcServer, ":0").
		WithShutdownChannel(shutdownChan)

	done := make(chan error, 1)

	go func() {
		done <- sm.StartWithGracefulShutdownWithError()
	}()

	close(shutdownChan)

	err := <-done
	assert.NoError(t, err, "StartWithGracefulShutdownWithError should complete without error")
}

func TestWithShutdownChannel(t *testing.T) {
	shutdownChan := make(chan struct{})
	sm := server.NewServerManager(nil, nil, nil).
		WithShutdownChannel(shutdownChan)
	assert.NotNil(t, sm, "WithShutdownChannel should return a non-nil instance")
}
