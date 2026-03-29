//go:build unit

// Copyright 2025 Lerian Studio.

package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultSnapshotFromKeyDefs_PopulatesConfigKeys(t *testing.T) {
	t.Parallel()

	defs := []KeyDef{
		{Key: "app.log_level", Kind: KindConfig, DefaultValue: "info", ValueType: ValueTypeString, ApplyBehavior: ApplyLiveRead, AllowedScopes: []Scope{ScopeGlobal}},
		{Key: "rate_limit.max", Kind: KindConfig, DefaultValue: 100, ValueType: ValueTypeInt, ApplyBehavior: ApplyLiveRead, AllowedScopes: []Scope{ScopeGlobal}},
	}

	snap := DefaultSnapshotFromKeyDefs(defs)

	require.Len(t, snap.Configs, 2)

	logLevel, ok := snap.Configs["app.log_level"]
	require.True(t, ok)
	assert.Equal(t, "info", logLevel.Value)
	assert.Equal(t, "info", logLevel.Default)
	assert.Equal(t, "registry-default", logLevel.Source)
	assert.Equal(t, RevisionZero, logLevel.Revision)
	assert.False(t, logLevel.Redacted)

	rateLimit, ok := snap.Configs["rate_limit.max"]
	require.True(t, ok)
	assert.Equal(t, 100, rateLimit.Value)
}

func TestDefaultSnapshotFromKeyDefs_SkipsNonConfigKinds(t *testing.T) {
	t.Parallel()

	defs := []KeyDef{
		{Key: "config.key", Kind: KindConfig, DefaultValue: "v", ValueType: ValueTypeString, ApplyBehavior: ApplyLiveRead, AllowedScopes: []Scope{ScopeGlobal}},
		{Key: "setting.key", Kind: KindSetting, DefaultValue: "v", ValueType: ValueTypeString, ApplyBehavior: ApplyLiveRead, AllowedScopes: []Scope{ScopeGlobal}},
	}

	snap := DefaultSnapshotFromKeyDefs(defs)

	require.Len(t, snap.Configs, 1)
	_, ok := snap.Configs["config.key"]
	assert.True(t, ok)
	_, ok = snap.Configs["setting.key"]
	assert.False(t, ok)
}

func TestDefaultSnapshotFromKeyDefs_MarksRedactedKeys(t *testing.T) {
	t.Parallel()

	defs := []KeyDef{
		{Key: "secret.key", Kind: KindConfig, DefaultValue: "", ValueType: ValueTypeString, ApplyBehavior: ApplyLiveRead, AllowedScopes: []Scope{ScopeGlobal}, RedactPolicy: RedactFull},
		{Key: "public.key", Kind: KindConfig, DefaultValue: "", ValueType: ValueTypeString, ApplyBehavior: ApplyLiveRead, AllowedScopes: []Scope{ScopeGlobal}, RedactPolicy: RedactNone},
		{Key: "masked.key", Kind: KindConfig, DefaultValue: "", ValueType: ValueTypeString, ApplyBehavior: ApplyLiveRead, AllowedScopes: []Scope{ScopeGlobal}, RedactPolicy: RedactMask},
		{Key: "empty.policy", Kind: KindConfig, DefaultValue: "", ValueType: ValueTypeString, ApplyBehavior: ApplyLiveRead, AllowedScopes: []Scope{ScopeGlobal}},
	}

	snap := DefaultSnapshotFromKeyDefs(defs)

	assert.True(t, snap.Configs["secret.key"].Redacted)
	assert.False(t, snap.Configs["public.key"].Redacted)
	assert.True(t, snap.Configs["masked.key"].Redacted)
	assert.False(t, snap.Configs["empty.policy"].Redacted)
}

func TestDefaultSnapshotFromKeyDefs_EmptyInput(t *testing.T) {
	t.Parallel()

	snap := DefaultSnapshotFromKeyDefs(nil)

	assert.Empty(t, snap.Configs)
	assert.False(t, snap.BuiltAt.IsZero())
}

func TestDefaultSnapshotFromKeyDefs_SetsBuiltAt(t *testing.T) {
	t.Parallel()

	snap := DefaultSnapshotFromKeyDefs([]KeyDef{
		{Key: "k", Kind: KindConfig, DefaultValue: "v", ValueType: ValueTypeString, ApplyBehavior: ApplyLiveRead, AllowedScopes: []Scope{ScopeGlobal}},
	})

	assert.False(t, snap.BuiltAt.IsZero())
	// Use zone offset (0 == UTC) instead of Location().String() to avoid
	// platform-specific timezone name differences (e.g., "UTC" vs "").
	_, offset := snap.BuiltAt.Zone()
	assert.Equal(t, 0, offset)
}

func TestDefaultSnapshotFromKeyDefs_SecretWithEmptyRedactPolicy(t *testing.T) {
	t.Parallel()

	// A key with Secret=true and no explicit RedactPolicy (zero value "").
	// The production code sets Redacted = def.Secret || (def.RedactPolicy != "" && def.RedactPolicy != RedactNone).
	// With Secret=true, Redacted must be true regardless of RedactPolicy.
	defs := []KeyDef{
		{
			Key:           "auth.token",
			Kind:          KindConfig,
			DefaultValue:  "",
			ValueType:     ValueTypeString,
			ApplyBehavior: ApplyBootstrapOnly,
			AllowedScopes: []Scope{ScopeGlobal},
			Secret:        true,
			RedactPolicy:  "", // explicitly empty — the "no policy declared" case
		},
	}

	snap := DefaultSnapshotFromKeyDefs(defs)

	entry, ok := snap.Configs["auth.token"]
	require.True(t, ok)
	assert.True(t, entry.Redacted, "Secret=true must force Redacted=true even when RedactPolicy is empty")
}
