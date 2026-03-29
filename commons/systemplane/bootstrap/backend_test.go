//go:build unit

// Copyright 2025 Lerian Studio.

// Tests in this file mutate the global backendRegistry (backendFactories and
// initErrors) to inject stubs and simulate error paths.  They must NOT use
// t.Parallel() at the top level to avoid data races on shared global state.
// Individual sub-tests that only read from the registry may be safe to
// parallelize, but the parent test functions are intentionally serial.

package bootstrap

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type noopStore struct{}

func (noopStore) Get(_ context.Context, _ domain.Target) (ports.ReadResult, error) {
	return ports.ReadResult{}, nil
}

func (noopStore) Put(
	_ context.Context,
	_ domain.Target,
	_ []ports.WriteOp,
	_ domain.Revision,
	_ domain.Actor,
	_ string,
) (domain.Revision, error) {
	return domain.RevisionZero, nil
}

type noopHistoryStore struct{}

func (noopHistoryStore) ListHistory(_ context.Context, _ ports.HistoryFilter) ([]ports.HistoryEntry, error) {
	return nil, nil
}

type noopChangeFeed struct{}

func (noopChangeFeed) Subscribe(_ context.Context, _ func(ports.ChangeSignal)) error {
	return nil
}

type noopCloser struct{}

func (noopCloser) Close() error { return nil }

// withRegistrySnapshot saves the global backendRegistry state and restores it
// via t.Cleanup so each test starts with a known state.
func withRegistrySnapshot(t *testing.T) {
	t.Helper()

	origFactories, origErrors := backendRegistry.snapshot()

	t.Cleanup(func() {
		backendRegistry.mu.Lock()
		defer backendRegistry.mu.Unlock()

		backendRegistry.factories = origFactories
		backendRegistry.initErrors = origErrors
	})
}

// registryDeleteLocked removes a factory from the registry under the mutex,
// keeping test mutations aligned with the production synchronization contract.
func registryDeleteLocked(kind domain.BackendKind) {
	backendRegistry.mu.Lock()
	defer backendRegistry.mu.Unlock()

	delete(backendRegistry.factories, kind)
}

// registrySetLocked overwrites a factory in the registry under the mutex.
func registrySetLocked(kind domain.BackendKind, factory BackendFactory) {
	backendRegistry.mu.Lock()
	defer backendRegistry.mu.Unlock()

	backendRegistry.factories[kind] = factory
}

func TestNewBackendFromConfig_NilConfig(t *testing.T) {
	res, err := NewBackendFromConfig(context.Background(), nil)

	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, errNilBackendConfig)
	assert.Contains(t, err.Error(), "config is nil")
}

func TestNewBackendFromConfig_MissingBackend(t *testing.T) {
	cfg := &BootstrapConfig{
		Backend: domain.BackendKind(""),
	}

	res, err := NewBackendFromConfig(context.Background(), cfg)

	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrMissingBackend)
}

func TestNewBackendFromConfig_PostgresMissingConfig(t *testing.T) {
	cfg := &BootstrapConfig{
		Backend:  domain.BackendPostgres,
		Postgres: nil,
	}

	res, err := NewBackendFromConfig(context.Background(), cfg)

	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrMissingPostgresConfig)
}

func TestNewBackendFromConfig_MongoMissingConfig(t *testing.T) {
	cfg := &BootstrapConfig{
		Backend: domain.BackendMongoDB,
		MongoDB: nil,
	}

	res, err := NewBackendFromConfig(context.Background(), cfg)

	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrMissingMongoConfig)
}

func TestNewBackendFromConfig_PostgresMissingDSN(t *testing.T) {
	cfg := &BootstrapConfig{
		Backend: domain.BackendPostgres,
		Postgres: &PostgresBootstrapConfig{
			DSN: "",
		},
	}

	res, err := NewBackendFromConfig(context.Background(), cfg)

	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrMissingPostgresDSN)
}

func TestNewBackendFromConfig_MongoMissingURI(t *testing.T) {
	cfg := &BootstrapConfig{
		Backend: domain.BackendMongoDB,
		MongoDB: &MongoBootstrapConfig{
			URI: "",
		},
	}

	res, err := NewBackendFromConfig(context.Background(), cfg)

	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrMissingMongoURI)
}

func TestNewBackendFromConfig_UnsupportedBackend(t *testing.T) {
	// "redis" is not a valid BackendKind, so Validate rejects it via
	// ErrMissingBackend before the factory lookup is even reached.
	cfg := &BootstrapConfig{
		Backend: domain.BackendKind("redis"),
	}

	res, err := NewBackendFromConfig(context.Background(), cfg)

	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "redis")
}

func TestNewBackendFromConfig_NoFactoryRegistered(t *testing.T) {
	// Temporarily remove the postgres factory to simulate an unregistered backend.
	withRegistrySnapshot(t)
	registryDeleteLocked(domain.BackendPostgres)

	cfg := &BootstrapConfig{
		Backend: domain.BackendPostgres,
		Postgres: &PostgresBootstrapConfig{
			DSN: "postgres://user:pass@localhost:5432/db",
		},
	}

	res, err := NewBackendFromConfig(context.Background(), cfg)

	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "no factory registered")
}

func TestNewBackendFromConfig_FactoryReturnsError(t *testing.T) {
	// Not parallel: mutates global backendFactories for postgres kind.
	testKind := domain.BackendPostgres
	withRegistrySnapshot(t)

	expectedErr := fmt.Errorf("simulated connection failure")
	registrySetLocked(testKind, func(_ context.Context, _ *BootstrapConfig) (*BackendResources, error) {
		return nil, expectedErr
	})

	cfg := &BootstrapConfig{
		Backend: testKind,
		Postgres: &PostgresBootstrapConfig{
			DSN: "postgres://user:pass@localhost:5432/db",
		},
	}

	res, err := NewBackendFromConfig(context.Background(), cfg)

	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, expectedErr)
}

func TestNewBackendFromConfig_FactoryReturnsResources(t *testing.T) {
	// Not parallel: mutates global backendFactories for postgres kind.
	testKind := domain.BackendPostgres
	withRegistrySnapshot(t)

	expected := &BackendResources{Store: noopStore{}, History: noopHistoryStore{}, ChangeFeed: noopChangeFeed{}, Closer: noopCloser{}}
	registrySetLocked(testKind, func(_ context.Context, _ *BootstrapConfig) (*BackendResources, error) {
		return expected, nil
	})

	cfg := &BootstrapConfig{
		Backend: testKind,
		Postgres: &PostgresBootstrapConfig{
			DSN: "postgres://user:pass@localhost:5432/db",
		},
	}

	res, err := NewBackendFromConfig(context.Background(), cfg)

	require.NoError(t, err)
	assert.Equal(t, expected, res)
}

func TestNewBackendFromConfig_FactoryReturnsIncompleteResources(t *testing.T) {
	// Not parallel: mutates global backendFactories for postgres kind.
	tests := []struct {
		name        string
		resources   *BackendResources
		expectedErr error
	}{
		{
			name:        "nil resources",
			resources:   nil,
			expectedErr: errNilBackendResources,
		},
		{
			name:        "nil store",
			resources:   &BackendResources{History: noopHistoryStore{}, ChangeFeed: noopChangeFeed{}, Closer: noopCloser{}},
			expectedErr: errNilBackendStore,
		},
		{
			name:        "nil history store",
			resources:   &BackendResources{Store: noopStore{}, ChangeFeed: noopChangeFeed{}, Closer: noopCloser{}},
			expectedErr: errNilBackendHistoryStore,
		},
		{
			name:        "nil change feed",
			resources:   &BackendResources{Store: noopStore{}, History: noopHistoryStore{}, Closer: noopCloser{}},
			expectedErr: errNilBackendChangeFeed,
		},
		{
			name:        "nil closer",
			resources:   &BackendResources{Store: noopStore{}, History: noopHistoryStore{}, ChangeFeed: noopChangeFeed{}},
			expectedErr: errNilBackendCloser,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testKind := domain.BackendPostgres
			withRegistrySnapshot(t)

			registrySetLocked(testKind, func(_ context.Context, _ *BootstrapConfig) (*BackendResources, error) {
				return tc.resources, nil
			})

			cfg := &BootstrapConfig{
				Backend: testKind,
				Postgres: &PostgresBootstrapConfig{
					DSN: "postgres://user:pass@localhost:5432/db",
				},
			}

			res, err := NewBackendFromConfig(context.Background(), cfg)

			require.Error(t, err)
			assert.Nil(t, res)
			assert.ErrorIs(t, err, tc.expectedErr)
		})
	}
}

func TestNewBackendFromConfig_AppliesDefaults(t *testing.T) {
	// Not parallel: mutates global backendFactories for postgres kind.
	testKind := domain.BackendPostgres
	withRegistrySnapshot(t)

	var capturedCfg *BootstrapConfig

	registrySetLocked(testKind, func(_ context.Context, cfg *BootstrapConfig) (*BackendResources, error) {
		capturedCfg = cfg
		return &BackendResources{Store: noopStore{}, History: noopHistoryStore{}, ChangeFeed: noopChangeFeed{}, Closer: noopCloser{}}, nil
	})

	cfg := &BootstrapConfig{
		Backend: testKind,
		Postgres: &PostgresBootstrapConfig{
			DSN: "postgres://user:pass@localhost:5432/db",
			// Leave Schema, EntriesTable, etc. empty — ApplyDefaults should fill them.
		},
	}

	_, err := NewBackendFromConfig(context.Background(), cfg)

	require.NoError(t, err)
	require.NotNil(t, capturedCfg)
	assert.Equal(t, "system", capturedCfg.Postgres.Schema)
	assert.Equal(t, "runtime_entries", capturedCfg.Postgres.EntriesTable)
	assert.Equal(t, "runtime_history", capturedCfg.Postgres.HistoryTable)
	assert.Equal(t, "runtime_revisions", capturedCfg.Postgres.RevisionTable)
	assert.Equal(t, "systemplane_changes", capturedCfg.Postgres.NotifyChannel)
}

func TestRegisterBackendFactory_RejectsOverwrite(t *testing.T) {
	testKind := domain.BackendPostgres
	withRegistrySnapshot(t)
	registryDeleteLocked(testKind)

	firstCalled := false
	secondCalled := false

	err := RegisterBackendFactory(testKind, func(_ context.Context, _ *BootstrapConfig) (*BackendResources, error) {
		firstCalled = true
		return &BackendResources{Store: noopStore{}, History: noopHistoryStore{}, ChangeFeed: noopChangeFeed{}, Closer: noopCloser{}}, nil
	})
	require.NoError(t, err)

	err = RegisterBackendFactory(testKind, func(_ context.Context, _ *BootstrapConfig) (*BackendResources, error) {
		secondCalled = true
		return &BackendResources{Store: noopStore{}, History: noopHistoryStore{}, ChangeFeed: noopChangeFeed{}, Closer: noopCloser{}}, nil
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, errBackendAlreadyRegistered)

	factories, _ := backendRegistry.snapshot()
	factory := factories[testKind]
	require.NotNil(t, factory)

	_, _ = factory(context.Background(), nil)

	assert.True(t, firstCalled, "first factory should remain registered")
	assert.False(t, secondCalled, "second factory should not be registered")
}

func TestRegisterBackendFactory_RejectsNilFactory(t *testing.T) {
	kind := domain.BackendMongoDB
	withRegistrySnapshot(t)
	registrySetLocked(kind, func(_ context.Context, _ *BootstrapConfig) (*BackendResources, error) {
		return &BackendResources{Store: noopStore{}, History: noopHistoryStore{}, ChangeFeed: noopChangeFeed{}, Closer: noopCloser{}}, nil
	})

	err := RegisterBackendFactory(kind, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, errNilBackendFactory)
	factories, _ := backendRegistry.snapshot()
	_, ok := factories[kind]
	assert.True(t, ok)
}

func TestRecordInitError_NilIsIgnored(t *testing.T) {
	withRegistrySnapshot(t)

	RecordInitError(nil)

	_, initErrors := backendRegistry.snapshot()
	assert.Empty(t, initErrors, "RecordInitError(nil) must not append to initErrors")
}

// ---------------------------------------------------------------------------
// MEDIUM-12: Verify ResetBackendFactories clears state for test isolation
// ---------------------------------------------------------------------------

func TestResetBackendFactories_ClearsRegistrations(t *testing.T) {
	// Not parallel: mutates global backendFactories.
	withRegistrySnapshot(t)

	// Register a factory and record an init error.
	registryDeleteLocked(domain.BackendPostgres)
	require.NoError(t, RegisterBackendFactory(domain.BackendPostgres, func(_ context.Context, _ *BootstrapConfig) (*BackendResources, error) {
		return &BackendResources{Store: noopStore{}, History: noopHistoryStore{}, ChangeFeed: noopChangeFeed{}, Closer: noopCloser{}}, nil
	}))

	RecordInitError(fmt.Errorf("simulated init error"))

	factories, initErrors := backendRegistry.snapshot()
	assert.Len(t, factories, 1)
	assert.NotEmpty(t, initErrors)

	// Reset and verify.
	ResetBackendFactories()

	factories, initErrors = backendRegistry.snapshot()
	assert.Empty(t, factories, "ResetBackendFactories should clear all factories")
	assert.Empty(t, initErrors, "ResetBackendFactories should clear init errors")
}

var _ io.Closer = noopCloser{}
