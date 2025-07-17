package license

import "sync"

// Handler defines the function signature for termination handlers
type Handler func(reason string)

// DefaultHandler is the default termination behavior
// It triggers a panic which will be caught by the graceful shutdown handler
func DefaultHandler(reason string) {
	panic("LICENSE VALIDATION FAILED: " + reason)
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
