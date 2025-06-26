package server

import "sync"

// Handler defines a function that handles license validation termination
type Handler func(reason string)

// DefaultHandler panics with a descriptive message
// This will be caught by the recover() in the application's graceful shutdown handler
func DefaultHandler(reason string) {
	panic("LICENSE VALIDATION FAILED: " + reason)
}

// LicenseManagerShutdown handles termination behavior
type LicenseManagerShutdown struct {
	handler Handler
	mu      sync.RWMutex
}

// New creates a new termination manager with the default handler
func New() *LicenseManagerShutdown {
	return &LicenseManagerShutdown{
		handler: DefaultHandler,
	}
}

// SetHandler updates the termination handler
// This should be called during application startup, before any validation occurs
func (m *LicenseManagerShutdown) SetHandler(handler Handler) {
	if handler == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.handler = handler
}

// Terminate invokes the termination handler
// This will trigger the application to gracefully shut down
func (m *LicenseManagerShutdown) Terminate(reason string) {
	m.mu.RLock()
	handler := m.handler
	m.mu.RUnlock()

	handler(reason)
}
