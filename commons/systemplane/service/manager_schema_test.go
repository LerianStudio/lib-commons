//go:build unit

// Copyright 2025 Lerian Studio.

package service

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetConfigSchema_ReturnsConfigKeysOnly(t *testing.T) {
	t.Parallel()

	reg, store, history, spy, builder := testManagerDeps(t)
	registerTestConfigKey(t, reg, "app.workers", domain.ApplyLiveRead, true)
	registerTestSettingKey(t, reg, "ui.theme", []domain.Scope{domain.ScopeGlobal}, true)

	mgr, mgrErr := NewManager(ManagerConfig{Registry: reg, Store: store, History: history, Supervisor: spy, Builder: builder})
	require.NoError(t, mgrErr)

	entries, err := mgr.GetConfigSchema(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "app.workers", entries[0].Key)
	assert.Equal(t, domain.KindConfig, entries[0].Kind)
}

func TestGetSettingSchema_ReturnsSettingKeysOnly(t *testing.T) {
	t.Parallel()

	reg, store, history, spy, builder := testManagerDeps(t)
	registerTestConfigKey(t, reg, "app.workers", domain.ApplyLiveRead, true)
	registerTestSettingKey(t, reg, "ui.theme", []domain.Scope{domain.ScopeGlobal, domain.ScopeTenant}, true)

	mgr, mgrErr := NewManager(ManagerConfig{Registry: reg, Store: store, History: history, Supervisor: spy, Builder: builder})
	require.NoError(t, mgrErr)

	entries, err := mgr.GetSettingSchema(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "ui.theme", entries[0].Key)
	assert.Equal(t, domain.KindSetting, entries[0].Kind)
}

func TestGetConfigSchema_RedactsSecretDefault(t *testing.T) {
	t.Parallel()

	reg, store, history, spy, builder := testManagerDeps(t)
	require.NoError(t, reg.Register(domain.KeyDef{
		Key:              "auth.token",
		EnvVar:           "AUTH_TOKEN",
		Kind:             domain.KindConfig,
		AllowedScopes:    []domain.Scope{domain.ScopeGlobal},
		ValueType:        domain.ValueTypeString,
		DefaultValue:     "my-token-value",
		Secret:           true,
		RedactPolicy:     domain.RedactFull,
		ApplyBehavior:    domain.ApplyLiveRead,
		MutableAtRuntime: true,
		Description:      "auth token",
		Group:            "auth",
	}))

	mgr, mgrErr := NewManager(ManagerConfig{Registry: reg, Store: store, History: history, Supervisor: spy, Builder: builder})
	require.NoError(t, mgrErr)

	entries, err := mgr.GetConfigSchema(context.Background())
	require.NoError(t, err)

	var found bool

	for _, entry := range entries {
		if entry.Key == "auth.token" {
			found = true
			assert.Equal(t, "****", entry.DefaultValue)
			assert.Equal(t, "AUTH_TOKEN", entry.EnvVar)
			assert.Equal(t, domain.RedactFull, entry.RedactPolicy)
		}
	}

	assert.True(t, found, "expected auth.token in schema")
}

func TestGetConfigSchema_ZeroValueRedactPolicyBecomesNone(t *testing.T) {
	t.Parallel()

	reg, store, history, spy, builder := testManagerDeps(t)
	require.NoError(t, reg.Register(domain.KeyDef{
		Key:              "app.name",
		EnvVar:           "APP_NAME",
		Kind:             domain.KindConfig,
		AllowedScopes:    []domain.Scope{domain.ScopeGlobal},
		ValueType:        domain.ValueTypeString,
		DefaultValue:     "service",
		ApplyBehavior:    domain.ApplyLiveRead,
		MutableAtRuntime: true,
		Description:      "app name",
		Group:            "app",
	}))

	mgr, mgrErr := NewManager(ManagerConfig{Registry: reg, Store: store, History: history, Supervisor: spy, Builder: builder})
	require.NoError(t, mgrErr)

	entries, err := mgr.GetConfigSchema(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, domain.RedactNone, entries[0].RedactPolicy)
}
