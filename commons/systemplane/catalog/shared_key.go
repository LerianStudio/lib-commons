// Copyright 2025 Lerian Studio.

package catalog

import "github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"

// SharedKey defines the canonical metadata for a cross-product config key.
// Products that register a key matching this catalog entry MUST use the
// same Key name, EnvVar, ApplyBehavior, Component, MutableAtRuntime,
// Secret, and RedactPolicy values.
type SharedKey struct {
	Key              string
	EnvVar           string // canonical env var (empty = varies by product)
	MatchEnvVars     []string
	ValueType        domain.ValueType
	ApplyBehavior    domain.ApplyBehavior
	MutableAtRuntime bool
	Component        string
	Group            string
	Secret           bool
	RedactPolicy     domain.RedactPolicy
	Description      string
}

func cloneSharedKeys(keys []SharedKey) []SharedKey {
	if keys == nil {
		return nil
	}

	cloned := make([]SharedKey, len(keys))
	copy(cloned, keys)

	for i := range cloned {
		cloned[i].MatchEnvVars = append([]string(nil), cloned[i].MatchEnvVars...)
	}

	return cloned
}
