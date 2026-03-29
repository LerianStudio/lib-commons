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
			Value:    def.DefaultValue,
			Default:  def.DefaultValue,
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
