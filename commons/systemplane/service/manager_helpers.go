// Copyright 2025 Lerian Studio.

package service

import (
	"reflect"
	"slices"
	"strings"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/ports"
	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/registry"
)

const (
	maskedValuePlaceholder = "****"
	visibleSuffixRuneCount = 4
)

func buildSchema(reg registry.Registry, kind domain.Kind) []SchemaEntry {
	defs := reg.List(kind)

	entries := make([]SchemaEntry, len(defs))

	for i, def := range defs {
		entries[i] = SchemaEntry{
			Key:              def.Key,
			EnvVar:           def.EnvVar,
			Kind:             def.Kind,
			AllowedScopes:    append([]domain.Scope(nil), def.AllowedScopes...),
			ValueType:        def.ValueType,
			DefaultValue:     redactValue(def, def.DefaultValue),
			MutableAtRuntime: def.MutableAtRuntime,
			ApplyBehavior:    def.ApplyBehavior,
			Secret:           def.Secret,
			RedactPolicy:     effectiveRedactPolicy(def),
			Description:      def.Description,
			Group:            def.Group,
		}
	}

	return entries
}

func cloneSnapshot(snapshot domain.Snapshot) domain.Snapshot {
	cloned := domain.Snapshot{
		Configs:        cloneEffectiveValues(snapshot.Configs),
		GlobalSettings: cloneEffectiveValues(snapshot.GlobalSettings),
		TenantSettings: make(map[string]map[string]domain.EffectiveValue, len(snapshot.TenantSettings)),
		Revision:       snapshot.Revision,
		BuiltAt:        snapshot.BuiltAt,
	}
	for tenantID, values := range snapshot.TenantSettings {
		cloned.TenantSettings[tenantID] = cloneEffectiveValues(values)
	}

	return cloned
}

func cloneEffectiveValues(values map[string]domain.EffectiveValue) map[string]domain.EffectiveValue {
	if values == nil {
		return nil
	}

	cloned := make(map[string]domain.EffectiveValue, len(values))

	for key, value := range values {
		value.Value = cloneRuntimeValue(value.Value)
		value.Default = cloneRuntimeValue(value.Default)
		value.Override = cloneRuntimeValue(value.Override)
		cloned[key] = value
	}

	return cloned
}

func cloneRuntimeValue(value any) any {
	if value == nil {
		return nil
	}

	rv := reflect.ValueOf(value)

	switch rv.Kind() {
	case reflect.Map:
		if rv.IsNil() {
			return value
		}

		cloned := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		for _, key := range rv.MapKeys() {
			cloned.SetMapIndex(key, cloneRuntimeReflectValue(rv.MapIndex(key), rv.Type().Elem()))
		}

		return cloned.Interface()
	case reflect.Slice:
		if rv.IsNil() {
			return value
		}

		cloned := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
		for index := range rv.Len() {
			cloned.Index(index).Set(cloneRuntimeReflectValue(rv.Index(index), rv.Type().Elem()))
		}

		return cloned.Interface()
	default:
		return value
	}
}

func cloneRuntimeReflectValue(value reflect.Value, target reflect.Type) reflect.Value {
	if !value.IsValid() {
		return reflect.Zero(target)
	}

	cloned := cloneRuntimeValue(value.Interface())
	if cloned == nil {
		return reflect.Zero(target)
	}

	clonedValue := reflect.ValueOf(cloned)
	if clonedValue.Type().AssignableTo(target) {
		return clonedValue
	}

	if clonedValue.Type().ConvertibleTo(target) {
		return clonedValue.Convert(target)
	}

	return value
}

// revisionFromValues returns the highest revision across all entries in the
// map. In practice all entries share the same revision (set by setRevision
// after each build), but using max() is a safety net against future callers
// that may not uphold that invariant.
func revisionFromValues(values map[string]domain.EffectiveValue) domain.Revision {
	maxRev := domain.RevisionZero
	for _, value := range values {
		if value.Revision > maxRev {
			maxRev = value.Revision
		}
	}

	return maxRev
}

func snapshotRevision(snapshot domain.Snapshot) domain.Revision {
	revision := revisionFromValues(snapshot.Configs)

	revision = maxRevisions(revision, revisionFromValues(snapshot.GlobalSettings))

	for _, values := range snapshot.TenantSettings {
		revision = maxRevisions(revision, revisionFromValues(values))
	}

	return revision
}

func redactEffectiveValues(reg registry.Registry, values map[string]domain.EffectiveValue) map[string]domain.EffectiveValue {
	for key, value := range values {
		def, ok := reg.Get(key)
		if !ok {
			continue
		}

		value.Value = redactValue(def, value.Value)
		value.Default = redactValue(def, value.Default)
		value.Override = redactValue(def, value.Override)
		values[key] = value
	}

	return values
}

func redactHistoryEntries(reg registry.Registry, entries []ports.HistoryEntry) []ports.HistoryEntry {
	redacted := make([]ports.HistoryEntry, len(entries))
	for i, entry := range entries {
		redacted[i] = entry

		def, ok := reg.Get(entry.Key)
		if !ok {
			continue
		}

		redacted[i].OldValue = redactValue(def, entry.OldValue)
		redacted[i].NewValue = redactValue(def, entry.NewValue)
	}

	return redacted
}

func redactValue(def domain.KeyDef, value any) any {
	if value == nil {
		return nil
	}

	policy := effectiveRedactPolicy(def)
	if policy == domain.RedactNone {
		return value
	}

	if policy == domain.RedactMask {
		return maskRedactedValue(value)
	}

	return maskedValuePlaceholder
}

func effectiveRedactPolicy(def domain.KeyDef) domain.RedactPolicy {
	if def.Secret {
		return domain.RedactFull
	}

	if def.RedactPolicy == "" {
		return domain.RedactNone
	}

	return def.RedactPolicy
}

func isRedacted(def domain.KeyDef) bool {
	return effectiveRedactPolicy(def) != domain.RedactNone
}

func maskRedactedValue(value any) any {
	stringValue, ok := value.(string)
	if !ok {
		return maskedValuePlaceholder
	}

	trimmed := strings.TrimSpace(stringValue)
	if trimmed == "" {
		return maskedValuePlaceholder
	}

	runes := []rune(trimmed)
	if len(runes) <= visibleSuffixRuneCount {
		return maskedValuePlaceholder
	}

	return strings.Repeat("*", len(runes)-visibleSuffixRuneCount) +
		string(runes[len(runes)-visibleSuffixRuneCount:])
}

func scopeAllowed(allowed []domain.Scope, target domain.Scope) bool {
	return slices.Contains(allowed, target)
}
