package server_test

import (
	"testing"

	"github.com/LerianStudio/lib-commons/commons/server"
	"github.com/stretchr/testify/assert"
)

func TestNew(t *testing.T) {
	manager := server.New()
	assert.NotNil(t, manager, "New should return a non-nil manager")
}

func TestSetHandler(t *testing.T) {
	manager := server.New()
	handlerCalled := false
	testHandler := func(reason string) {
		handlerCalled = true
		assert.Equal(t, "test reason", reason)
	}

	manager.SetHandler(testHandler)
	manager.Terminate("test reason")

	assert.True(t, handlerCalled, "Custom handler should have been called")
}

func TestSetHandlerWithNil(t *testing.T) {
	manager := server.New()
	handlerCalled := false
	testHandler := func(reason string) {
		handlerCalled = true
	}
	
	manager.SetHandler(testHandler)
	manager.SetHandler(nil)
	manager.Terminate("test")
	
	assert.True(t, handlerCalled, "Original handler should still be active after attempting to set nil handler")
}

func TestDefaultHandler(t *testing.T) {
	manager := server.New()
	
	assert.Panics(t, func() {
		manager.Terminate("default handler test")
	}, "Default handler should panic with license validation failure message")
}
