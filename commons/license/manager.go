package license

import (
	"fmt"
	"os"
	"sync"
)

// Handler defines the function signature for termination handlers
type Handler func(reason string)

// DefaultHandler is the default termination behavior.
// It logs the failure reason to stderr and terminates the process with exit code 1.
// This ensures the application cannot continue running with an invalid license,
// even when a recovery middleware is present that would catch panics.
func DefaultHandler(reason string) {
	fmt.Fprintf(os.Stderr, "LICENSE VALIDATION FAILED: %s\n", reason)
	os.Exit(1)
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

// Terminate invokes the termination handler
// This will trigger the application to gracefully shut down
func (m *ManagerShutdown) Terminate(reason string) {
	m.mu.RLock()
	handler := m.handler
	m.mu.RUnlock()

	if handler == nil {
		panic("license.ManagerShutdown used without initialization. Use license.New() to create an instance.")
	}

	handler(reason)
}
