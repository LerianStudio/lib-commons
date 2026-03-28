// Copyright 2025 Lerian Studio.

package service

import "github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"

func buildResolvedSet(values map[string]domain.EffectiveValue) ResolvedSet {
	return ResolvedSet{
		Values:   values,
		Revision: revisionFromValues(values),
	}
}

func (manager *defaultManager) resolvedConfigsFromSnapshot(snapshot domain.Snapshot) (ResolvedSet, bool) {
	if snapshot.BuiltAt.IsZero() || snapshot.Configs == nil {
		return ResolvedSet{}, false
	}

	values := redactEffectiveValues(manager.registry, cloneEffectiveValues(snapshot.Configs))

	return buildResolvedSet(values), true
}

func (manager *defaultManager) resolvedSettingsFromSnapshot(snapshot domain.Snapshot, subject Subject) (ResolvedSet, bool) {
	if snapshot.BuiltAt.IsZero() {
		return ResolvedSet{}, false
	}

	return manager.cachedSettingsFromSnapshot(snapshot, subject)
}
