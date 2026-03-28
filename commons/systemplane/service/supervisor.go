// Copyright 2025 Lerian Studio.

package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	libOpentelemetry "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/ports"
)

var (
	errSupervisorBuilderRequired = errors.New("new supervisor: snapshot builder is required")
	errSupervisorFactoryRequired = errors.New("new supervisor: bundle factory is required")
	errSupervisorNilReconciler   = errors.New("new supervisor: reconciler is nil")
)

// Supervisor manages the runtime bundle lifecycle with atomic snapshot/bundle swaps.
type Supervisor interface {
	Current() domain.RuntimeBundle
	Snapshot() domain.Snapshot
	PublishSnapshot(ctx context.Context, snap domain.Snapshot, reason string) error
	ReconcileCurrent(ctx context.Context, snap domain.Snapshot, reason string) error
	Reload(ctx context.Context, reason string, extraTenantIDs ...string) error
	Stop(ctx context.Context) error
}

// BuildStrategy describes which build path the Supervisor took during a reload.
type BuildStrategy string

const (
	// BuildStrategyFull indicates all bundle components were built from scratch.
	BuildStrategyFull BuildStrategy = "full"

	// BuildStrategyIncremental indicates only changed components were rebuilt;
	// unchanged components were reused from the previous bundle.
	BuildStrategyIncremental BuildStrategy = "incremental"
)

// ReloadEvent carries structured information about a completed reload cycle.
// Passed to the optional Observer callback on SupervisorConfig.
type ReloadEvent struct {
	Strategy BuildStrategy // which build path was taken
	Reason   string        // caller-supplied reason (e.g., "changefeed-signal")
	Snapshot domain.Snapshot
	Bundle   domain.RuntimeBundle
}

// SupervisorConfig holds the dependencies needed to construct a supervisor.
type SupervisorConfig struct {
	Builder     *SnapshotBuilder
	Factory     ports.BundleFactory
	Reconcilers []ports.BundleReconciler

	// Observer is an optional callback invoked after each successful reload
	// with structured information about the build strategy used. This
	// provides observability without coupling the Supervisor to a logger.
	// Nil means no observation.
	Observer func(ReloadEvent)
}

// NewSupervisor creates a new supervisor.
func NewSupervisor(cfg SupervisorConfig) (Supervisor, error) {
	if cfg.Builder == nil {
		return nil, fmt.Errorf("%w", errSupervisorBuilderRequired)
	}

	if domain.IsNilValue(cfg.Factory) {
		return nil, fmt.Errorf("%w", errSupervisorFactoryRequired)
	}

	for i, reconciler := range cfg.Reconcilers {
		if isNilReconciler(reconciler) {
			return nil, fmt.Errorf("%w: index %d", errSupervisorNilReconciler, i)
		}
	}

	sorted := sortReconcilersByPhase(cfg.Reconcilers)

	return &defaultSupervisor{
		builder:     cfg.Builder,
		factory:     cfg.Factory,
		reconcilers: sorted,
		observer:    cfg.Observer,
		stopCh:      make(chan struct{}),
	}, nil
}

// supervisorState holds the immutable (snapshot, bundle) pair that readers
// observe via a single atomic pointer. Replacing two separate atomics with
// one guarantees readers always see a consistent pair.
type supervisorState struct {
	snapshot domain.Snapshot
	bundle   domain.RuntimeBundle
}

type defaultSupervisor struct {
	state       atomic.Pointer[supervisorState]
	mu          sync.Mutex
	builder     *SnapshotBuilder
	factory     ports.BundleFactory
	reconcilers []ports.BundleReconciler
	observer    func(ReloadEvent)
	stopCh      chan struct{}
	stopOnce    sync.Once
}

// Current returns the currently active runtime bundle.
func (supervisor *defaultSupervisor) Current() domain.RuntimeBundle {
	st := supervisor.state.Load()
	if st == nil || isNilRuntimeBundle(st.bundle) {
		return nil
	}

	return st.bundle
}

// Snapshot returns the latest published snapshot.
func (supervisor *defaultSupervisor) Snapshot() domain.Snapshot {
	st := supervisor.state.Load()
	if st == nil {
		return domain.Snapshot{}
	}

	return st.snapshot
}

// PublishSnapshot publishes a snapshot without rebuilding bundles.
// The context and reason parameters are part of the Supervisor contract and are
// reserved for future tracing/audit hooks even though the current implementation
// only needs the snapshot payload.
func (supervisor *defaultSupervisor) PublishSnapshot(ctx context.Context, snap domain.Snapshot, _ string) error {
	_, span := startSupervisorSpan(ctx, "publish_snapshot")
	defer span.End()

	if supervisor.isStopped() {
		libOpentelemetry.HandleSpanError(span, "supervisor stopped", domain.ErrSupervisorStopped)
		return domain.ErrSupervisorStopped
	}

	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()

	prev := supervisor.state.Load()

	newState := &supervisorState{snapshot: snap}
	if prev != nil {
		newState.bundle = prev.bundle
	}

	supervisor.state.Store(newState)

	return nil
}

// ReconcileCurrent reconciles the current bundle against a provided snapshot.
// The reason parameter is reserved for future tracing/audit hooks while the
// current implementation only needs the context and snapshot.
func (supervisor *defaultSupervisor) ReconcileCurrent(ctx context.Context, snap domain.Snapshot, _ string) error {
	ctx, span := startSupervisorSpan(ctx, "reconcile_current")
	defer span.End()

	if supervisor.isStopped() {
		libOpentelemetry.HandleSpanError(span, "supervisor stopped", domain.ErrSupervisorStopped)
		return domain.ErrSupervisorStopped
	}

	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()

	prev := supervisor.state.Load()
	if prev == nil || isNilRuntimeBundle(prev.bundle) {
		libOpentelemetry.HandleSpanError(span, "missing current bundle", domain.ErrNoCurrentBundle)
		return domain.ErrNoCurrentBundle
	}

	// Optimistically swap to new snapshot, keeping the same bundle.
	supervisor.state.Store(&supervisorState{snapshot: snap, bundle: prev.bundle})

	for _, reconciler := range supervisor.reconcilers {
		if err := reconciler.Reconcile(ctx, prev.bundle, prev.bundle, snap); err != nil {
			// Rollback: restore previous state atomically.
			supervisor.state.Store(prev)

			libOpentelemetry.HandleSpanError(span, "reconcile current bundle", err)

			return fmt.Errorf("%s: %w: %w", reconciler.Name(), domain.ErrReconcileFailed, err)
		}
	}

	return nil
}

// Reload rebuilds the snapshot and runtime bundle, then reconciles consumers.
// Optional extraTenantIDs are merged into the cached tenant list to ensure
// first-seen tenants (not yet in any snapshot) are included in the rebuild.
func (supervisor *defaultSupervisor) Reload(ctx context.Context, reason string, extraTenantIDs ...string) error {
	ctx, span := startSupervisorSpan(ctx, "reload")
	defer span.End()

	if supervisor.isStopped() {
		libOpentelemetry.HandleSpanError(span, "supervisor stopped", domain.ErrSupervisorStopped)
		return domain.ErrSupervisorStopped
	}

	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()

	var prevSnap *domain.Snapshot
	if st := supervisor.state.Load(); st != nil {
		prevSnap = &st.snapshot
	}

	tenantIDs := mergeUniqueTenantIDs(cachedTenantIDs(prevSnap), extraTenantIDs)

	build, err := supervisor.prepareReloadBuild(ctx, tenantIDs)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "build runtime bundle", err)
		return err
	}

	if isNilRuntimeBundle(build.candidate) {
		libOpentelemetry.HandleSpanError(span, "nil runtime bundle", domain.ErrBundleBuildFailed)
		return fmt.Errorf("reload: %w: nil runtime bundle", domain.ErrBundleBuildFailed)
	}

	// Reconcile BEFORE committing: run all reconcilers against the candidate
	// bundle while the previous bundle is still the active one. This prevents
	// state corruption when incremental builds nil-out transferred pointers in
	// the previous bundle — if we stored the candidate first and a reconciler
	// failed, the "rollback" would restore a gutted previous bundle.
	if err := supervisor.reconcileCandidateBundle(ctx, build); err != nil {
		libOpentelemetry.HandleSpanError(span, "reconcile candidate bundle", err)
		return err
	}

	// All reconcilers passed — commit atomically.
	supervisor.commitReload(ctx, reason, build)

	return nil
}

// Stop terminates supervisor operations and closes the active bundle.
func (supervisor *defaultSupervisor) Stop(ctx context.Context) error {
	ctx, span := startSupervisorSpan(ctx, "stop")
	defer span.End()

	supervisor.stopOnce.Do(func() {
		close(supervisor.stopCh)
	})

	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()

	st := supervisor.state.Load()
	if st != nil && !isNilRuntimeBundle(st.bundle) {
		if err := st.bundle.Close(ctx); err != nil {
			libOpentelemetry.HandleSpanError(span, "close current bundle", err)
			return fmt.Errorf("stop close current bundle: %w", err)
		}
	}

	return nil
}
