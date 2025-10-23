package license_test

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v3/commons/license"
	"github.com/stretchr/testify/assert"
)

func TestNew(t *testing.T) {
	manager := license.New()
	assert.NotNil(t, manager, "New should return a non-nil manager")
}

func TestSetHandler(t *testing.T) {
	manager := license.New()
	handlerCalled := false
	testHandler := func(reason string) {
		handlerCalled = true
	}

	manager.SetHandler(testHandler)
	manager.Terminate("test")

	assert.True(t, handlerCalled, "Custom handler should be called")
}

func TestSetHandlerWithNil(t *testing.T) {
	manager := license.New()
	handlerCalled := false
	testHandler := func(reason string) {
		handlerCalled = true
	}

	manager.SetHandler(testHandler)
	manager.SetHandler(nil) // This should not change the handler
	manager.Terminate("test")

	assert.True(t, handlerCalled, "Original handler should still be called when nil is passed")
}

func TestDefaultHandler(t *testing.T) {
	manager := license.New()

	assert.Panics(t, func() {
		manager.Terminate("default handler test")
	}, "Default handler should panic")
}
