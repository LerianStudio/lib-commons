// Copyright 2025 Lerian Studio.

// Package bootstrap wires systemplane backends from bootstrap configuration.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"sync"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/ports"
)

// BackendResources holds the backend family created by the backend factory.
type BackendResources struct {
	Store      ports.Store
	History    ports.HistoryStore
	ChangeFeed ports.ChangeFeed
	Closer     io.Closer
}

// BackendFactory constructs the runtime backend family from the bootstrap
// configuration. The function is injected to avoid import cycles between the
// bootstrap and adapter packages.
type BackendFactory func(ctx context.Context, cfg *BootstrapConfig) (*BackendResources, error)

type backendRegistryState struct {
	mu         sync.RWMutex
	factories  map[domain.BackendKind]BackendFactory
	initErrors []error
}

func newBackendRegistryState() *backendRegistryState {
	return &backendRegistryState{factories: map[domain.BackendKind]BackendFactory{}}
}

func (s *backendRegistryState) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.factories = map[domain.BackendKind]BackendFactory{}
	s.initErrors = nil
}

func (s *backendRegistryState) recordInitError(err error) {
	if domain.IsNilValue(err) {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.initErrors = append(s.initErrors, err)
}

func (s *backendRegistryState) register(kind domain.BackendKind, factory BackendFactory) error {
	if !kind.IsValid() {
		return fmt.Errorf("%w %q", errInvalidBackendKind, kind)
	}

	if factory == nil {
		return errNilBackendFactory
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.factories[kind]; exists {
		return fmt.Errorf("%w %q", errBackendAlreadyRegistered, kind)
	}

	s.factories[kind] = factory

	return nil
}

func (s *backendRegistryState) snapshot() (map[domain.BackendKind]BackendFactory, []error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	factories := make(map[domain.BackendKind]BackendFactory, len(s.factories))
	maps.Copy(factories, s.factories)

	return factories, append([]error(nil), s.initErrors...)
}

var backendRegistry = newBackendRegistryState()

var (
	errNilBackendConfig         = errors.New("bootstrap backend: config is nil")
	errUnsupportedBackend       = errors.New("bootstrap backend: unsupported backend")
	errNilBackendResources      = errors.New("bootstrap backend: factory returned nil resources")
	errNilBackendStore          = errors.New("bootstrap backend: factory returned nil store")
	errNilBackendHistoryStore   = errors.New("bootstrap backend: factory returned nil history store")
	errNilBackendChangeFeed     = errors.New("bootstrap backend: factory returned nil change feed")
	errNilBackendCloser         = errors.New("bootstrap backend: factory returned nil closer")
	errInvalidBackendKind       = errors.New("bootstrap backend: invalid backend kind")
	errNilBackendFactory        = errors.New("bootstrap backend: factory is nil")
	errBackendAlreadyRegistered = errors.New("bootstrap backend: factory already registered")
)

// ResetBackendFactories clears all registered backend factories and init
// errors. This function exists only for test isolation and must not be called
// in production code.
func ResetBackendFactories() {
	backendRegistry.reset()
}

// RecordInitError appends an error to the package-level init error list.
// It is intended to be called from init() functions in adapter packages when
// RegisterBackendFactory fails.
func RecordInitError(err error) {
	backendRegistry.recordInitError(err)
}

// RegisterBackendFactory registers a BackendFactory for the given backend kind.
// It is intended to be called from adapter package init() functions or from
// the application wiring code that imports the adapter packages.
//
// Registration is single-write per backend kind. Duplicate or nil
// registrations are rejected to preserve bootstrap integrity.
func RegisterBackendFactory(kind domain.BackendKind, factory BackendFactory) error {
	return backendRegistry.register(kind, factory)
}

// NewBackendFromConfig creates the backend family based on the configured
// backend kind. It validates the config, applies defaults, and delegates to the
// registered BackendFactory for the configured backend.
//
// The caller is responsible for calling Closer.Close when the resources are no
// longer needed. Callers typically defer res.Closer.Close().
func NewBackendFromConfig(ctx context.Context, cfg *BootstrapConfig) (*BackendResources, error) {
	factories, initErrors := backendRegistry.snapshot()
	if len(initErrors) > 0 {
		return nil, fmt.Errorf("bootstrap backend: init registration errors: %w", errors.Join(initErrors...))
	}

	if cfg == nil {
		return nil, errNilBackendConfig
	}

	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("bootstrap backend: %w", err)
	}

	factory, ok := factories[cfg.Backend]
	if !ok {
		return nil, fmt.Errorf("%w %q (no factory registered)", errUnsupportedBackend, cfg.Backend)
	}

	resources, err := factory(ctx, cfg)
	if err != nil {
		return nil, err
	}

	if err := validateBackendResources(resources); err != nil {
		return nil, err
	}

	return resources, nil
}

func validateBackendResources(resources *BackendResources) error {
	if resources == nil {
		return errNilBackendResources
	}

	if domain.IsNilValue(resources.Store) {
		return errNilBackendStore
	}

	if domain.IsNilValue(resources.History) {
		return errNilBackendHistoryStore
	}

	if domain.IsNilValue(resources.ChangeFeed) {
		return errNilBackendChangeFeed
	}

	if domain.IsNilValue(resources.Closer) {
		return errNilBackendCloser
	}

	return nil
}
