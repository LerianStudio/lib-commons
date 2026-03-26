//go:build unit

// Copyright 2025 Lerian Studio.

package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
)

func TestCloneSnapshot_DeepClonesNestedRuntimeValues(t *testing.T) {
	t.Parallel()

	snapshot := domain.Snapshot{
		Configs: map[string]domain.EffectiveValue{
			"feature.flags": {
				Key: "feature.flags",
				Value: map[string]any{
					"enabled": true,
					"nested":  []any{"alpha", map[string]any{"beta": true}},
				},
				Default: map[string]any{"enabled": false},
				Override: []any{
					map[string]any{"kind": "manual"},
				},
			},
		},
	}

	cloned := cloneSnapshot(snapshot)

	valueMap, ok := cloned.Configs["feature.flags"].Value.(map[string]any)
	require.True(t, ok)
	nestedSlice, ok := valueMap["nested"].([]any)
	require.True(t, ok)
	nestedMap, ok := nestedSlice[1].(map[string]any)
	require.True(t, ok)

	defaultMap, ok := cloned.Configs["feature.flags"].Default.(map[string]any)
	require.True(t, ok)
	overrideSlice, ok := cloned.Configs["feature.flags"].Override.([]any)
	require.True(t, ok)
	overrideMap, ok := overrideSlice[0].(map[string]any)
	require.True(t, ok)

	nestedMap["beta"] = false
	defaultMap["enabled"] = true
	overrideMap["kind"] = "automatic"

	originalValueMap, ok := snapshot.Configs["feature.flags"].Value.(map[string]any)
	require.True(t, ok, "original Value must be map[string]any")
	originalNestedSlice, ok := originalValueMap["nested"].([]any)
	require.True(t, ok, "original nested must be []any")
	originalNestedMap, ok := originalNestedSlice[1].(map[string]any)
	require.True(t, ok, "original nested[1] must be map[string]any")
	originalDefaultMap, ok := snapshot.Configs["feature.flags"].Default.(map[string]any)
	require.True(t, ok, "original Default must be map[string]any")
	originalOverrideSlice, ok := snapshot.Configs["feature.flags"].Override.([]any)
	require.True(t, ok, "original Override must be []any")
	originalOverrideMap, ok := originalOverrideSlice[0].(map[string]any)
	require.True(t, ok, "original Override[0] must be map[string]any")

	assert.Equal(t, true, originalNestedMap["beta"])
	assert.Equal(t, false, originalDefaultMap["enabled"])
	assert.Equal(t, "manual", originalOverrideMap["kind"])
}

func TestRedactValue_RedactMaskMasksStringTail(t *testing.T) {
	t.Parallel()

	masked := redactValue(domain.KeyDef{RedactPolicy: domain.RedactMask}, "sk_live_12345678")

	assert.Equal(t, "************5678", masked)
}

func TestRedactValue_RedactMaskFallsBackForNonString(t *testing.T) {
	t.Parallel()

	masked := redactValue(domain.KeyDef{RedactPolicy: domain.RedactMask}, map[string]any{"token": "abc"})

	assert.Equal(t, "****", masked)
}

func TestRedactValue_SecretDefaultsToFullRedaction(t *testing.T) {
	t.Parallel()

	masked := redactValue(domain.KeyDef{Secret: true}, "amqp://user:pass@host")

	assert.Equal(t, "****", masked)
}

func TestEffectiveRedactPolicy_NormalizesZeroValue(t *testing.T) {
	t.Parallel()

	assert.Equal(t, domain.RedactNone, effectiveRedactPolicy(domain.KeyDef{}))
	assert.Equal(t, domain.RedactFull, effectiveRedactPolicy(domain.KeyDef{Secret: true}))
}
