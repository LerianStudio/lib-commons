// Copyright 2025 Lerian Studio.

package service

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"go.opentelemetry.io/otel/attribute"
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

// recordCleanupError records a best-effort cleanup error to the active span.
// Cleanup failures are not actionable by callers but must not go invisible.
func recordCleanupError(ctx context.Context, phase string, err error) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.RecordError(err, trace.WithAttributes(
			attribute.String("cleanup.phase", phase),
		))
	}
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

		if err := discarder.Discard(ctx); err != nil {
			recordCleanupError(ctx, "discard_incremental_candidate", err)
		}

		return
	}

	if discarder, ok := candidate.(rollbackDiscarder); ok {
		if err := discarder.Discard(ctx); err != nil {
			recordCleanupError(ctx, "discard_full_candidate", err)
		}

		return
	}

	if err := candidate.Close(ctx); err != nil {
		recordCleanupError(ctx, "close_failed_candidate", err)
	}
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
		// Use the incremental discard path so shared/adopted resources are
		// released safely rather than double-closed via a plain Close().
		if err != nil && !isNilRuntimeBundle(candidate) {
			discardFailedCandidate(ctx, candidate, BuildStrategyIncremental)
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

// prepareReloadBuild builds a new snapshot and candidate bundle for a reload.
//
// Concurrency safety: BuildIncremental receives a reference to the live
// previousBundle. Its contract is to create a candidate that SHARES resources
// (read-only) from the previous bundle — it must NOT mutate the previous
// bundle's internal pointers. The actual resource transfer (nil-ing
// transferred pointers in previousBundle) happens later in commitReload via
// AdoptResourcesFrom, which runs AFTER the atomic state swap. At that point
// Current() already returns the new candidate, so concurrent readers never
// observe the mutation.
func (supervisor *defaultSupervisor) prepareReloadBuild(ctx context.Context, tenantIDs []string, currentState *supervisorState) (reloadBuild, error) {
	snap, err := supervisor.builder.BuildFull(ctx, tenantIDs...)
	if err != nil {
		return reloadBuild{}, fmt.Errorf("reload: %w: %w", domain.ErrSnapshotBuildFailed, err)
	}

	var prevSnap *domain.Snapshot

	var previousBundle domain.RuntimeBundle

	if currentState != nil {
		prevSnap = &currentState.snapshot
		previousBundle = currentState.bundle
	}

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
	supervisor.state.Store(&supervisorState{
		snapshot: build.snapshot,
		bundle:   build.candidate,
	})

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
		if err := build.previousBundle.Close(ctx); err != nil {
			recordCleanupError(ctx, "close_previous_bundle", err)
		}
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
