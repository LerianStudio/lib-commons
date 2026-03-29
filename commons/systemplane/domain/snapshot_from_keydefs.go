// Copyright 2025 Lerian Studio.

package domain

import "time"

// DefaultSnapshotFromKeyDefs builds a Snapshot pre-seeded with the default
// values declared in the given KeyDefs. Only KindConfig definitions are
// included; settings and tenant-scoped keys are skipped.
//
// The resulting snapshot has RevisionZero and Source "registry-default" for
// every entry. This is useful for tests, pre-store bootstrap, and any
// context where a SnapshotBuilder (which requires a live Store) is not yet
// available.
func DefaultSnapshotFromKeyDefs(defs []KeyDef) Snapshot {
	configs := make(map[string]EffectiveValue, len(defs))

	for _, def := range defs {
		if def.Kind != KindConfig {
			continue
		}

		configs[def.Key] = EffectiveValue{
			Key:      def.Key,
			Value:    cloneRuntimeValue(def.DefaultValue),
			Default:  cloneRuntimeValue(def.DefaultValue),
			Source:   "registry-default",
			Revision: RevisionZero,
			Redacted: def.Secret || (def.RedactPolicy != "" && def.RedactPolicy != RedactNone),
		}
	}

	return Snapshot{
		Configs:        configs,
		GlobalSettings: make(map[string]EffectiveValue),
		TenantSettings: make(map[string]map[string]EffectiveValue),
		BuiltAt:        time.Now().UTC(),
	}
}

// cloneRuntimeValue returns a deep copy for map[string]any and []any values
// to prevent aliasing between the runtime Value and the stored Default in
// an EffectiveValue. Scalar types (string, int, bool, etc.) are returned as-is
// since they are immutable.
func cloneRuntimeValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = cloneRuntimeValue(vv)
		}

		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = cloneRuntimeValue(vv)
		}

		return out
	default:
		return v
	}
}
