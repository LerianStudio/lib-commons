// Copyright 2025 Lerian Studio.

package service

import (
	"reflect"
	"sort"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
)

// ComponentDiff computes which infrastructure components need rebuilding
// by comparing effective values between two snapshots.
type ComponentDiff struct {
	allComponents []string
	keyMeta       map[string]componentKeyMeta
}

type componentKeyMeta struct {
	component             string
	triggersBundleRebuild bool
	invalidApplyBehavior  bool
	unclassified          bool
}

// NewComponentDiff builds a diff engine from key definitions.
// Keys with rebuild-triggering apply behaviors and empty Component are tracked
// as unclassified so ChangedComponents can force a full rebuild for safety.
// Invalid apply behaviors are also treated conservatively when a changed key is
// encountered.
func NewComponentDiff(defs []domain.KeyDef) *ComponentDiff {
	keyMeta := make(map[string]componentKeyMeta, len(defs))
	componentSet := make(map[string]struct{}, len(defs))

	for _, def := range defs {
		meta := componentKeyMeta{
			component:             def.Component,
			triggersBundleRebuild: triggersBundleRebuild(def.ApplyBehavior),
			invalidApplyBehavior:  !def.ApplyBehavior.IsValid(),
			unclassified:          triggersBundleRebuild(def.ApplyBehavior) && def.Component == "",
		}
		keyMeta[def.Key] = meta

		if def.Component != "" && def.Component != domain.ComponentNone {
			componentSet[def.Component] = struct{}{}
		}
	}

	components := make([]string, 0, len(componentSet))
	for component := range componentSet {
		components = append(components, component)
	}

	sort.Strings(components)

	return &ComponentDiff{
		allComponents: components,
		keyMeta:       keyMeta,
	}
}

// triggersBundleRebuild reports whether the given apply behavior requires a
// component rebuild. Only bundle-rebuild and bundle-rebuild+worker-reconcile
// qualify; all other behaviors propagate without infrastructure teardown.
func triggersBundleRebuild(ab domain.ApplyBehavior) bool {
	switch ab {
	case domain.ApplyBundleRebuild, domain.ApplyBundleRebuildAndReconcile:
		return true
	default:
		return false
	}
}

// ChangedComponents returns the set of component names that have at least
// one key whose effective value differs between prev and current snapshots.
// Configs, global settings, and tenant settings are all considered.
// If prev is zero-value, ALL components are returned (full rebuild).
// Unknown keys, invalid apply behaviors, or unclassified rebuild-triggering
// keys also force a full rebuild for safety.
func (d *ComponentDiff) ChangedComponents(prev, current domain.Snapshot) map[string]bool {
	if d == nil || len(d.allComponents) == 0 {
		return map[string]bool{}
	}

	if snapshotIsZeroValue(prev) {
		return d.allComponentSet()
	}

	changed := make(map[string]bool)
	if d.markChangedComponents(prev.Configs, current.Configs, changed) {
		return d.allComponentSet()
	}

	if d.markChangedComponents(prev.GlobalSettings, current.GlobalSettings, changed) {
		return d.allComponentSet()
	}

	if d.markChangedTenantSettings(prev.TenantSettings, current.TenantSettings, changed) {
		return d.allComponentSet()
	}

	return changed
}

// AllComponents returns every distinct component name in the mapping, sorted.
func (d *ComponentDiff) AllComponents() []string {
	if d == nil || len(d.allComponents) == 0 {
		return []string{}
	}

	return append([]string(nil), d.allComponents...)
}

func snapshotIsZeroValue(snapshot domain.Snapshot) bool {
	return snapshot.Revision == domain.RevisionZero &&
		snapshot.BuiltAt.IsZero() &&
		len(snapshot.Configs) == 0 &&
		len(snapshot.GlobalSettings) == 0 &&
		len(snapshot.TenantSettings) == 0
}

func (d *ComponentDiff) markChangedTenantSettings(prev, current map[string]map[string]domain.EffectiveValue, changed map[string]bool) bool {
	tenantIDs := make(map[string]struct{}, len(prev)+len(current))
	for tenantID := range prev {
		tenantIDs[tenantID] = struct{}{}
	}

	for tenantID := range current {
		tenantIDs[tenantID] = struct{}{}
	}

	for tenantID := range tenantIDs {
		if d.markChangedComponents(prev[tenantID], current[tenantID], changed) {
			return true
		}
	}

	return false
}

func (d *ComponentDiff) markChangedComponents(prevEntries, currentEntries map[string]domain.EffectiveValue, changed map[string]bool) bool {
	keys := make(map[string]struct{}, len(prevEntries)+len(currentEntries))
	for key := range prevEntries {
		keys[key] = struct{}{}
	}

	for key := range currentEntries {
		keys[key] = struct{}{}
	}

	for key := range keys {
		prevVal, prevOK := prevEntries[key]

		curVal, curOK := currentEntries[key]
		if !effectiveValueChanged(prevVal, prevOK, curVal, curOK) {
			continue
		}

		meta, found := d.keyMeta[key]
		if !found {
			return true
		}

		if meta.invalidApplyBehavior {
			return true
		}

		if !meta.triggersBundleRebuild {
			continue
		}

		if meta.unclassified {
			return true
		}

		if meta.component == domain.ComponentNone {
			continue
		}

		changed[meta.component] = true
	}

	return false
}

// allComponentSet returns a bool map of every component — used for full
// rebuild when there is no previous snapshot to diff against.
func (d *ComponentDiff) allComponentSet() map[string]bool {
	set := make(map[string]bool, len(d.allComponents))

	for _, component := range d.allComponents {
		set[component] = true
	}

	return set
}

// effectiveValueChanged reports whether the effective value changed between two
// snapshots. It uses reflect.DeepEqual on EffectiveValue.Value, which means:
//   - nil slice != empty slice (both are valid "no items" representations)
//   - function values are always unequal
//   - supported types: primitives, string slices, maps — the types produced by
//     the coercion layer. If other types are stored, callers must normalize
//     before comparison.
func effectiveValueChanged(prevVal domain.EffectiveValue, prevOK bool, curVal domain.EffectiveValue, curOK bool) bool {
	if prevOK != curOK {
		return true
	}

	if !prevOK {
		return false
	}

	return !reflect.DeepEqual(prevVal.Value, curVal.Value)
}
