package license

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/LerianStudio/lib-commons/v5/commons/assert"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
)

var (
	// ErrLicenseValidationFailed represents a license validation failure
	ErrLicenseValidationFailed = errors.New("license validation failed")
	// ErrManagerNotInitialized indicates the manager was used without proper initialization
	ErrManagerNotInitialized = errors.New("license.ManagerShutdown used without initialization: use license.New() to create an instance")
)

// Handler defines the function signature for termination handlers
type Handler func(reason string)

// ManagerOption is a functional option for configuring ManagerShutdown.
type ManagerOption func(*ManagerShutdown)

// WithLogger provides a structured logger for assertion and validation logging.
func WithLogger(l log.Logger) ManagerOption {
	return func(m *ManagerShutdown) {
		if l != nil {
			m.Logger = l
		}
	}
}

// WithFailClosed configures the manager to log the reason at error level and
// terminate the process when license validation fails.
func WithFailClosed() ManagerOption {
	return func(m *ManagerShutdown) {
		m.handler = func(reason string) {
			if m.Logger != nil {
				m.Logger.Log(context.Background(), log.LevelError, "license validation failed (fail-closed mode)",
					log.String("reason", reason),
				)
			}

			DefaultHandler(reason)
		}
	}
}

// DefaultHandler is the default termination behavior.
// It logs the failure reason to stderr and terminates the process with exit code 1.
// This ensures the application cannot continue running with an invalid license.
func DefaultHandler(reason string) {
	fmt.Fprintf(os.Stderr, "LICENSE VALIDATION FAILED: %s\n", reason)
	os.Exit(1)
}

// DefaultHandlerWithError returns an error instead of panicking.
// Use this when you want to handle license failures gracefully.
func DefaultHandlerWithError(reason string) error {
	return fmt.Errorf("%w: %s", ErrLicenseValidationFailed, reason)
}

// ManagerShutdown handles termination behavior
type ManagerShutdown struct {
	handler Handler
	Logger  log.Logger
	mu      sync.RWMutex
}

// New creates a new termination manager with the default handler.
// Options can be provided to configure the manager (e.g., WithLogger).
// Nil options in the variadic list are silently skipped.
func New(opts ...ManagerOption) *ManagerShutdown {
	m := &ManagerShutdown{
		handler: DefaultHandler,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}

	return m
}

// SetHandler updates the termination handler
// This should be called during application startup, before any validation occurs
func (m *ManagerShutdown) SetHandler(handler Handler) {
	if m == nil || handler == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.handler = handler
}

// Terminate invokes the termination handler.
// This will trigger the application to gracefully shut down.
//
// Note: This method no longer panics if the manager was not initialized with New().
// In that case it records an assertion failure and returns.
func (m *ManagerShutdown) Terminate(reason string) {
	if m == nil {
		// nil receiver: no logger available, nil is legitimate here.
		asserter := assert.New(context.Background(), nil, "license", "Terminate")
		_ = asserter.Never(context.Background(), "license.ManagerShutdown is nil")

		return
	}

	m.mu.RLock()
	handler := m.handler
	logger := m.Logger
	m.mu.RUnlock()

	if handler == nil {
		asserter := assert.New(context.Background(), logger, "license", "Terminate")
		_ = asserter.NoError(context.Background(), ErrManagerNotInitialized,
			"license terminate called without initialization",
			"reason", reason,
		)

		return
	}

	handler(reason)
}

// TerminateWithError returns an error instead of invoking the termination handler.
// Use this when you want to check license validity without triggering shutdown.
//
// Note: This method intentionally does NOT invoke the custom handler set via SetHandler().
// It always returns ErrLicenseValidationFailed wrapped with the reason when the
// manager is properly initialized. Use Terminate() for actual shutdown behavior,
// and TerminateWithError() for validation checks that should return errors.
//
// Nil receiver: returns ErrManagerNotInitialized (not ErrLicenseValidationFailed)
// to distinguish between "license failed" and "manager not created".
func (m *ManagerShutdown) TerminateWithError(reason string) error {
	if m == nil {
		return ErrManagerNotInitialized
	}

	if m.Logger != nil {
		m.Logger.Log(context.Background(), log.LevelWarn, "license validation failed",
			log.String("reason", reason),
		)
	}

	return fmt.Errorf("%w: %s", ErrLicenseValidationFailed, reason)
}

// TerminateSafe invokes the termination handler and returns an error if the manager
// was not properly initialized. This is the safe alternative to Terminate that
// returns an explicit error when the handler is nil.
//
// Use this method when you need to handle the uninitialized manager case gracefully.
// For normal shutdown behavior where assertion-based handling is acceptable,
// use Terminate() instead.
func (m *ManagerShutdown) TerminateSafe(reason string) error {
	if m == nil {
		return ErrManagerNotInitialized
	}

	m.mu.RLock()
	handler := m.handler
	logger := m.Logger
	m.mu.RUnlock()

	if handler == nil {
		if logger != nil {
			logger.Log(context.Background(), log.LevelWarn, "license terminate called without initialization",
				log.String("reason", reason),
			)
		}

		return ErrManagerNotInitialized
	}

	handler(reason)

	return nil
}
