//go:build unit

package server_test

import (
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/server"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
)

// TestServersStarted_NilReceiver covers the nil receiver guard on ServersStarted.
func TestServersStarted_NilReceiver(t *testing.T) {
	t.Parallel()

	var sm *server.ServerManager
	ch := sm.ServersStarted()
	// Should return a closed channel for nil receiver
	select {
	case <-ch:
		// closed channel - expected
	default:
		t.Fatal("expected channel to be closed for nil receiver")
	}
}

// TestWithHTTPServer_NilReceiver covers nil receiver guard.
func TestWithHTTPServer_NilReceiver(t *testing.T) {
	t.Parallel()

	var sm *server.ServerManager
	result := sm.WithHTTPServer(fiber.New(), ":0")
	assert.Nil(t, result)
}

// TestWithGRPCServer_NilReceiver covers nil receiver guard.
func TestWithGRPCServer_NilReceiver(t *testing.T) {
	t.Parallel()

	var sm *server.ServerManager
	result := sm.WithGRPCServer(nil, ":0")
	assert.Nil(t, result)
}

// TestWithShutdownChannel_NilReceiver covers nil receiver guard.
func TestWithShutdownChannel_NilReceiver(t *testing.T) {
	t.Parallel()

	var sm *server.ServerManager
	ch := make(chan struct{})
	result := sm.WithShutdownChannel(ch)
	assert.Nil(t, result)
}

// TestWithShutdownTimeout_NilReceiver covers nil receiver guard.
func TestWithShutdownTimeout_NilReceiver(t *testing.T) {
	t.Parallel()

	var sm *server.ServerManager
	result := sm.WithShutdownTimeout(5 * time.Second)
	assert.Nil(t, result)
}
