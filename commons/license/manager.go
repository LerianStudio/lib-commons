package license

import (
	"errors"
	"fmt"
	"sync"
)

var (
	// ErrLicenseValidationFailed represents a license validation failure
	ErrLicenseValidationFailed = errors.New("license validation failed")
	// ErrManagerNotInitialized indicates the manager was used without proper initialization
	ErrManagerNotInitialized = errors.New("license.ManagerShutdown used without initialization: use license.New() to create an instance")
)

// Handler defines the function signature for termination handlers
type Handler func(reason string)

// DefaultHandler is the default termination behavior
// It triggers a panic which will be caught by the graceful shutdown handler
func DefaultHandler(reason string) {
	panic("LICENSE VALIDATION FAILED: " + reason)
}

// DefaultHandlerWithError returns an error instead of panicking.
// Use this when you want to handle license failures gracefully.
func DefaultHandlerWithError(reason string) error {
	return fmt.Errorf("%w: %s", ErrLicenseValidationFailed, reason)
}

// ManagerShutdown handles termination behavior
type ManagerShutdown struct {
	handler Handler
	mu      sync.RWMutex
}

// New creates a new termination manager with the default handler
func New() *ManagerShutdown {
	return &ManagerShutdown{
		handler: DefaultHandler,
	}
}

// SetHandler updates the termination handler
// This should be called during application startup, before any validation occurs
func (m *ManagerShutdown) SetHandler(handler Handler) {
	if handler == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.handler = handler
}

// Terminate invokes the termination handler.
// This will trigger the application to gracefully shut down.
//
// Note: This method panics if the manager was not initialized with New().
// Use TerminateSafe() if you need to handle the uninitialized case gracefully.
func (m *ManagerShutdown) Terminate(reason string) {
	m.mu.RLock()
	handler := m.handler
	m.mu.RUnlock()

	if handler == nil {
		panic(ErrManagerNotInitialized)
	}

	handler(reason)
}

// TerminateWithError returns an error instead of invoking the termination handler.
// Use this when you want to check license validity without triggering shutdown.
//
// Note: This method intentionally does NOT invoke the custom handler set via SetHandler().
// It always returns ErrLicenseValidationFailed wrapped with the reason, regardless of
// manager initialization state. This differs from Terminate() which requires initialization
// and invokes the configured handler. Use Terminate() for actual shutdown behavior,
// and TerminateWithError() for validation checks that should return errors.
func (m *ManagerShutdown) TerminateWithError(reason string) error {
	return fmt.Errorf("%w: %s", ErrLicenseValidationFailed, reason)
}

// TerminateSafe invokes the termination handler and returns an error if the manager
// was not properly initialized. This is the safe alternative to Terminate that
// returns an error instead of panicking when the handler is nil.
//
// Use this method when you need to handle the uninitialized manager case gracefully.
// For normal shutdown behavior where panic on uninitialized manager is acceptable,
// use Terminate() instead.
func (m *ManagerShutdown) TerminateSafe(reason string) error {
	m.mu.RLock()
	handler := m.handler
	m.mu.RUnlock()

	if handler == nil {
		return ErrManagerNotInitialized
	}

	handler(reason)

	return nil
}
