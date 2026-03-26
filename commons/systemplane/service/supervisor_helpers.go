// Copyright 2025 Lerian Studio.

package service

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"go.opentelemetry.io/otel/trace"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/ports"
)

type resourceAdopter interface {
	AdoptResourcesFrom(previous domain.RuntimeBundle)
}

type rollbackDiscarder interface {
	Discard(ctx context.Context) error
}

type reloadBuild struct {
	snapshot       domain.Snapshot
	previousSnap   *domain.Snapshot
	previousBundle domain.RuntimeBundle
	candidate      domain.RuntimeBundle
	strategy       BuildStrategy
}

func discardFailedCandidate(ctx context.Context, candidate domain.RuntimeBundle, strategy BuildStrategy) {
	if isNilRuntimeBundle(candidate) {
		return
	}

	if strategy == BuildStrategyIncremental {
		discarder, ok := candidate.(rollbackDiscarder)
		if !ok {
			// Incremental candidates may already share adopted resources with the
			// previous bundle. Without an explicit discard contract, avoid closing
			// them here and let the factory-specific cleanup path decide.
			return
		}

		_ = discarder.Discard(ctx) // Best-effort rollback cleanup; errors are not actionable here.

		return
	}

	if discarder, ok := candidate.(rollbackDiscarder); ok {
		_ = discarder.Discard(ctx) // Best-effort rollback cleanup; errors are not actionable here.

		return
	}

	_ = candidate.Close(ctx) // Best-effort close of failed candidate; caller cannot recover from close errors.
}

func startSupervisorSpan(ctx context.Context, operation string) (context.Context, trace.Span) {
	_, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)           //nolint:dogsled
	ctx, span := tracer.Start(ctx, "systemplane.supervisor."+operation) //nolint:spancheck // Caller owns the returned span lifecycle.

	return ctx, span //nolint:spancheck // Caller owns the returned span lifecycle.
}

func (supervisor *defaultSupervisor) isStopped() bool {
	select {
	case <-supervisor.stopCh:
		return true
	default:
		return false
	}
}

func isNilRuntimeBundle(bundle domain.RuntimeBundle) bool {
	return domain.IsNilValue(bundle)
}

func isNilReconciler(reconciler ports.BundleReconciler) bool {
	return domain.IsNilValue(reconciler)
}

// buildBundle attempts an incremental build first (if the factory supports it
// and a previous bundle exists), falling back to a full build. Returns the
// build strategy used for observability.
func (supervisor *defaultSupervisor) buildBundle(
	ctx context.Context,
	snap domain.Snapshot,
	previousBundle domain.RuntimeBundle,
	prevSnap *domain.Snapshot,
) (domain.RuntimeBundle, BuildStrategy, error) {
	// Incremental path: reuse unchanged components from the previous bundle.
	if incFactory, ok := supervisor.factory.(ports.IncrementalBundleFactory); ok &&
		prevSnap != nil && !isNilRuntimeBundle(previousBundle) {
		candidate, err := incFactory.BuildIncremental(ctx, snap, previousBundle, *prevSnap)
		if err == nil && !isNilRuntimeBundle(candidate) {
			return candidate, BuildStrategyIncremental, nil
		}

		// Discard partially-built candidate to prevent resource leaks.
		if err != nil && !isNilRuntimeBundle(candidate) {
			// RuntimeBundle.Close(ctx) is the contract for releasing held resources.
			_ = candidate.Close(ctx) // Best-effort: discard partial candidate to prevent resource leaks.
		}
		// Incremental build failed — fall through to full build.
	}

	// Full build: construct everything from scratch.
	bundle, err := supervisor.factory.Build(ctx, snap)
	if err != nil {
		return nil, BuildStrategyFull, fmt.Errorf("build full bundle: %w", err)
	}

	return bundle, BuildStrategyFull, nil
}

func currentBundle(holder *bundleHolder) domain.RuntimeBundle {
	if holder == nil {
		return nil
	}

	return holder.bundle
}

func (supervisor *defaultSupervisor) prepareReloadBuild(ctx context.Context, tenantIDs []string) (reloadBuild, error) {
	snap, err := supervisor.builder.BuildFull(ctx, tenantIDs...)
	if err != nil {
		return reloadBuild{}, fmt.Errorf("reload: %w: %w", domain.ErrSnapshotBuildFailed, err)
	}

	prevSnap := supervisor.snapshot.Load()
	previousBundle := currentBundle(supervisor.bundle.Load())

	candidate, strategy, err := supervisor.buildBundle(ctx, snap, previousBundle, prevSnap)
	if err != nil {
		return reloadBuild{}, fmt.Errorf("reload: %w: %w", domain.ErrBundleBuildFailed, err)
	}

	return reloadBuild{
		snapshot:       snap,
		previousSnap:   prevSnap,
		previousBundle: previousBundle,
		candidate:      candidate,
		strategy:       strategy,
	}, nil
}

func (supervisor *defaultSupervisor) reconcileCandidateBundle(ctx context.Context, build reloadBuild) error {
	for _, reconciler := range supervisor.reconcilers {
		if err := reconciler.Reconcile(ctx, build.previousBundle, build.candidate, build.snapshot); err != nil {
			discardFailedCandidate(ctx, build.candidate, build.strategy)

			return fmt.Errorf("reload: %s: %w: %w", reconciler.Name(), domain.ErrReconcileFailed, err)
		}
	}

	return nil
}

func (supervisor *defaultSupervisor) commitReload(ctx context.Context, reason string, build reloadBuild) {
	supervisor.snapshot.Store(&build.snapshot)
	supervisor.bundle.Store(&bundleHolder{bundle: build.candidate})

	if adopter, ok := build.candidate.(resourceAdopter); ok && !isNilRuntimeBundle(build.previousBundle) {
		adopter.AdoptResourcesFrom(build.previousBundle)
	}

	if supervisor.observer != nil {
		supervisor.observer(ReloadEvent{
			Strategy: build.strategy,
			Reason:   reason,
			Snapshot: build.snapshot,
			Bundle:   build.candidate,
		})
	}

	if !isNilRuntimeBundle(build.previousBundle) {
		_ = build.previousBundle.Close(ctx) // Best-effort: previous bundle superseded; close errors are not actionable.
	}
}

// sortReconcilersByPhase returns a copy of the reconciler slice sorted by
// phase in ascending order (StateSync → Validation → SideEffect). Reconcilers
// within the same phase retain their original relative order (stable sort).
func sortReconcilersByPhase(reconcilers []ports.BundleReconciler) []ports.BundleReconciler {
	sorted := make([]ports.BundleReconciler, len(reconcilers))
	copy(sorted, reconcilers)

	slices.SortStableFunc(sorted, func(a, b ports.BundleReconciler) int {
		ap, bp := int(a.Phase()), int(b.Phase())
		if ap < bp {
			return -1
		}

		if ap > bp {
			return 1
		}

		return 0
	})

	return sorted
}

// mergeUniqueTenantIDs merges extra tenant IDs into the base list, deduplicating
// and sorting the result. This ensures first-seen tenants (not yet in any
// snapshot) are included in bundle rebuilds.
func mergeUniqueTenantIDs(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}

	seen := make(map[string]struct{}, len(base)+len(extra))

	// Build result from a fresh slice to avoid mutating the caller's base.
	result := make([]string, 0, len(base)+len(extra))

	for _, id := range base {
		seen[id] = struct{}{}
		result = append(result, id)
	}

	added := false

	for _, id := range extra {
		if id == "" {
			continue
		}

		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			result = append(result, id)
			added = true
		}
	}

	// If nothing new was added, return the original base to preserve
	// its nil/empty semantics (callers may check for nil).
	if !added {
		return base
	}

	sort.Strings(result)

	return result
}

func cachedTenantIDs(snapshot *domain.Snapshot) []string {
	if snapshot == nil || len(snapshot.TenantSettings) == 0 {
		return nil
	}

	tenantIDs := make([]string, 0, len(snapshot.TenantSettings))

	for tenantID := range snapshot.TenantSettings {
		tenantIDs = append(tenantIDs, tenantID)
	}

	sort.Strings(tenantIDs)

	return tenantIDs
}
