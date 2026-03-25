//go:build unit

// Copyright 2025 Lerian Studio.

package catalog

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helper builds a minimal valid KeyDef from a SharedKey so tests only need to
// override the field under test.
func keyDefFromShared(sk SharedKey) domain.KeyDef {
	return domain.KeyDef{
		Key:              sk.Key,
		EnvVar:           sk.EnvVar,
		Kind:             domain.KindConfig,
		AllowedScopes:    []domain.Scope{domain.ScopeGlobal},
		ValueType:        sk.ValueType,
		Secret:           sk.Secret,
		RedactPolicy:     sk.RedactPolicy,
		ApplyBehavior:    sk.ApplyBehavior,
		MutableAtRuntime: sk.MutableAtRuntime,
		Component:        sk.Component,
		Group:            sk.Group,
		Description:      sk.Description,
	}
}

func TestValidateKeyDefs_AllMatch(t *testing.T) {
	// Build product defs that exactly mirror a subset of the catalog.
	catalogSlice := []SharedKey{
		{Key: "postgres.primary_host", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "postgres"},
		{Key: "redis.host", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis"},
	}

	productDefs := make([]domain.KeyDef, len(catalogSlice))
	for i, sk := range catalogSlice {
		productDefs[i] = keyDefFromShared(sk)
	}

	mismatches := ValidateKeyDefs(productDefs, catalogSlice)
	assert.Empty(t, mismatches, "expected zero mismatches when product matches catalog exactly")
}

func TestValidateKeyDefs_ApplyBehaviorMismatch(t *testing.T) {
	catalogSlice := []SharedKey{
		{Key: "postgres.max_open_conns", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone},
	}

	pd := keyDefFromShared(catalogSlice[0])
	pd.ApplyBehavior = domain.ApplyBundleRebuild // drift!

	mismatches := ValidateKeyDefs([]domain.KeyDef{pd}, catalogSlice)
	require.Len(t, mismatches, 1)
	assert.Equal(t, "postgres.max_open_conns", mismatches[0].CatalogKey)
	assert.Equal(t, "ApplyBehavior", mismatches[0].Field)
	assert.Equal(t, string(domain.ApplyLiveRead), mismatches[0].CatalogValue)
	assert.Equal(t, string(domain.ApplyBundleRebuild), mismatches[0].ProductValue)
}

func TestValidateKeyDefs_ComponentMismatch(t *testing.T) {
	catalogSlice := []SharedKey{
		{Key: "redis.host", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis"},
	}

	pd := keyDefFromShared(catalogSlice[0])
	pd.Component = "cache" // wrong component name

	mismatches := ValidateKeyDefs([]domain.KeyDef{pd}, catalogSlice)
	require.Len(t, mismatches, 1)
	assert.Equal(t, "Component", mismatches[0].Field)
	assert.Equal(t, "redis", mismatches[0].CatalogValue)
	assert.Equal(t, "cache", mismatches[0].ProductValue)
}

func TestValidateKeyDefs_MutableAtRuntimeMismatch(t *testing.T) {
	catalogSlice := []SharedKey{
		{Key: "app.log_level", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone},
	}

	pd := keyDefFromShared(catalogSlice[0])
	pd.MutableAtRuntime = false // drift!

	mismatches := ValidateKeyDefs([]domain.KeyDef{pd}, catalogSlice)
	require.Len(t, mismatches, 1)
	assert.Equal(t, "MutableAtRuntime", mismatches[0].Field)
	assert.Equal(t, "true", mismatches[0].CatalogValue)
	assert.Equal(t, "false", mismatches[0].ProductValue)
}

func TestValidateKeyDefs_ValueTypeMismatch(t *testing.T) {
	catalogSlice := []SharedKey{
		{Key: "postgres.primary_port", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "postgres"},
	}

	pd := keyDefFromShared(catalogSlice[0])
	pd.ValueType = domain.ValueTypeString // oops, string instead of int

	mismatches := ValidateKeyDefs([]domain.KeyDef{pd}, catalogSlice)
	require.Len(t, mismatches, 1)
	assert.Equal(t, "ValueType", mismatches[0].Field)
	assert.Equal(t, string(domain.ValueTypeInt), mismatches[0].CatalogValue)
	assert.Equal(t, string(domain.ValueTypeString), mismatches[0].ProductValue)
}

func TestValidateKeyDefs_KeyMismatchMatchedByEnvVar(t *testing.T) {
	catalogSlice := []SharedKey{
		{Key: "postgres.primary_ssl_mode", EnvVar: "POSTGRES_SSLMODE", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "postgres"},
	}

	pd := keyDefFromShared(catalogSlice[0])
	pd.Key = "postgres.primary_sslmode"

	mismatches := ValidateKeyDefs([]domain.KeyDef{pd}, catalogSlice)
	require.Len(t, mismatches, 1)
	assert.Equal(t, "Key", mismatches[0].Field)
	assert.Equal(t, "postgres.primary_ssl_mode", mismatches[0].CatalogValue)
	assert.Equal(t, "postgres.primary_sslmode", mismatches[0].ProductValue)
}

func TestValidateKeyDefs_KeyMismatchMatchedByAlternateEnvVar(t *testing.T) {
	catalogSlice := []SharedKey{
		{Key: "auth.client_secret", MatchEnvVars: []string{"AUTH_CLIENT_SECRET", "PLUGIN_AUTH_CLIENT_SECRET"}, ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBootstrapOnly, Secret: true, RedactPolicy: domain.RedactFull},
	}

	pd := keyDefFromShared(catalogSlice[0])
	pd.Key = "plugin.auth_client_secret"
	pd.EnvVar = "PLUGIN_AUTH_CLIENT_SECRET"

	mismatches := ValidateKeyDefs([]domain.KeyDef{pd}, catalogSlice)
	require.Len(t, mismatches, 1)
	assert.Equal(t, "Key", mismatches[0].Field)
	assert.Equal(t, "auth.client_secret", mismatches[0].CatalogValue)
	assert.Equal(t, "plugin.auth_client_secret", mismatches[0].ProductValue)
}

func TestValidateKeyDefs_EnvVarMismatch(t *testing.T) {
	catalogSlice := []SharedKey{
		{Key: "redis.host", EnvVar: "REDIS_HOST", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis"},
	}

	pd := keyDefFromShared(catalogSlice[0])
	pd.EnvVar = "CACHE_HOST"

	mismatches := ValidateKeyDefs([]domain.KeyDef{pd}, catalogSlice)
	require.Len(t, mismatches, 1)
	assert.Equal(t, "EnvVar", mismatches[0].Field)
	assert.Equal(t, "REDIS_HOST", mismatches[0].CatalogValue)
	assert.Equal(t, "CACHE_HOST", mismatches[0].ProductValue)
}

func TestValidateKeyDefs_SecretMismatch(t *testing.T) {
	catalogSlice := []SharedKey{{Key: "auth.client_secret", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBootstrapOnly, Secret: true, RedactPolicy: domain.RedactFull}}
	pd := keyDefFromShared(catalogSlice[0])
	pd.Secret = false

	mismatches := ValidateKeyDefs([]domain.KeyDef{pd}, catalogSlice)
	require.Len(t, mismatches, 1)
	assert.Equal(t, "Secret", mismatches[0].Field)
}

func TestValidateKeyDefs_RedactPolicyMismatch(t *testing.T) {
	catalogSlice := []SharedKey{{Key: "auth.client_secret", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBootstrapOnly, Secret: true, RedactPolicy: domain.RedactFull}}
	pd := keyDefFromShared(catalogSlice[0])
	pd.RedactPolicy = domain.RedactMask

	mismatches := ValidateKeyDefs([]domain.KeyDef{pd}, catalogSlice)
	require.Len(t, mismatches, 1)
	assert.Equal(t, "RedactPolicy", mismatches[0].Field)
}

func TestValidateKeyDefs_ProductOnlyKeys_NotFlagged(t *testing.T) {
	catalogSlice := []SharedKey{
		{Key: "redis.host", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis"},
	}

	productDefs := []domain.KeyDef{
		keyDefFromShared(catalogSlice[0]),
		{
			Key:              "my_product.custom_feature",
			Kind:             domain.KindConfig,
			AllowedScopes:    []domain.Scope{domain.ScopeGlobal},
			ValueType:        domain.ValueTypeBool,
			ApplyBehavior:    domain.ApplyLiveRead,
			MutableAtRuntime: true,
			Component:        domain.ComponentNone,
		},
	}

	mismatches := ValidateKeyDefs(productDefs, catalogSlice)
	assert.Empty(t, mismatches, "product-only keys must not produce mismatches")
}

func TestValidateKeyDefs_CatalogOnlyKeys_NotFlagged(t *testing.T) {
	catalogSlice := []SharedKey{
		{Key: "redis.host", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis"},
		{Key: "redis.password", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis"},
		{Key: "redis.db", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "redis"},
	}

	// Product only uses redis.host — the other two catalog keys are unused.
	productDefs := []domain.KeyDef{
		keyDefFromShared(catalogSlice[0]),
	}

	mismatches := ValidateKeyDefs(productDefs, catalogSlice)
	assert.Empty(t, mismatches, "catalog-only keys must not produce mismatches")
}

func TestValidateKeyDefs_MultipleMismatches(t *testing.T) {
	catalogSlice := []SharedKey{
		{Key: "postgres.max_open_conns", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone},
	}

	pd := keyDefFromShared(catalogSlice[0])
	pd.ApplyBehavior = domain.ApplyBundleRebuild // wrong
	pd.MutableAtRuntime = false                  // wrong
	pd.ValueType = domain.ValueTypeString        // wrong
	pd.Component = "postgres"                    // wrong (catalog says ComponentNone)

	mismatches := ValidateKeyDefs([]domain.KeyDef{pd}, catalogSlice)
	require.Len(t, mismatches, 4, "expected four mismatches for four divergent fields")

	// Verify all fields are represented (sorted by field name within same key).
	fields := make(map[string]bool)
	for _, m := range mismatches {
		fields[m.Field] = true
		assert.Equal(t, "postgres.max_open_conns", m.CatalogKey)
	}

	assert.True(t, fields["ApplyBehavior"])
	assert.True(t, fields["Component"])
	assert.True(t, fields["MutableAtRuntime"])
	assert.True(t, fields["ValueType"])
}

func TestValidateKeyDefs_EmptyInputs(t *testing.T) {
	tests := []struct {
		name        string
		productDefs []domain.KeyDef
		catalog     [][]SharedKey
	}{
		{name: "nil product defs", productDefs: nil, catalog: [][]SharedKey{PostgresKeys()}},
		{name: "empty product defs", productDefs: []domain.KeyDef{}, catalog: [][]SharedKey{RedisKeys()}},
		{name: "nil catalog", productDefs: []domain.KeyDef{{Key: "x"}}, catalog: nil},
		{name: "empty catalog slice", productDefs: []domain.KeyDef{{Key: "x"}}, catalog: [][]SharedKey{{}}},
		{name: "both empty", productDefs: nil, catalog: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mismatches := ValidateKeyDefs(tt.productDefs, tt.catalog...)
			assert.Empty(t, mismatches)
		})
	}
}

func TestAllSharedKeys_NoDuplicates(t *testing.T) {
	all := AllSharedKeys()
	seen := make(map[string]int, len(all))

	for i, sk := range all {
		if prev, exists := seen[sk.Key]; exists {
			t.Errorf("duplicate key %q: first at index %d, again at index %d", sk.Key, prev, i)
		}

		seen[sk.Key] = i
	}
}

func TestAllSharedKeys_AllValid(t *testing.T) {
	for _, sk := range AllSharedKeys() {
		t.Run(sk.Key, func(t *testing.T) {
			assert.NotEmpty(t, sk.Key, "SharedKey must have a non-empty Key")
			assert.True(t, sk.ValueType.IsValid(), "ValueType %q is invalid", sk.ValueType)
			assert.True(t, sk.ApplyBehavior.IsValid(), "ApplyBehavior %q is invalid", sk.ApplyBehavior)
			assert.True(t, sk.RedactPolicy.IsValid(), "RedactPolicy %q is invalid", sk.RedactPolicy)
			if sk.Secret {
				assert.NotEqual(t, domain.RedactMask, sk.RedactPolicy, "secret key %q must not use mask redaction", sk.Key)
			}
			assert.NotEmpty(t, sk.Description, "SharedKey %q must have a Description", sk.Key)
		})
	}
}

func TestAllSharedKeys_NoDuplicateEnvVars(t *testing.T) {
	seen := make(map[string]string)

	for _, sk := range AllSharedKeys() {
		for _, envVar := range allowedEnvVars(sk) {
			if envVar == "" {
				continue
			}

			if existingKey, ok := seen[envVar]; ok {
				t.Fatalf("duplicate env var %q used by %q and %q", envVar, existingKey, sk.Key)
			}

			seen[envVar] = sk.Key
		}
	}
}

func TestMismatch_String(t *testing.T) {
	tests := []struct {
		name     string
		mismatch Mismatch
		want     string
	}{
		{
			name: "same key name",
			mismatch: Mismatch{
				CatalogKey:   "redis.host",
				ProductKey:   "redis.host",
				Field:        "ApplyBehavior",
				CatalogValue: "live-read",
				ProductValue: "bundle-rebuild",
			},
			want: `key "redis.host": ApplyBehavior: want live-read, got bundle-rebuild`,
		},
		{
			name: "different key names",
			mismatch: Mismatch{
				CatalogKey:   "postgres.primary_host",
				ProductKey:   "pg.host",
				Field:        "Component",
				CatalogValue: "postgres",
				ProductValue: "pg",
			},
			want: `key "pg.host" (catalog: "postgres.primary_host"): Component: want postgres, got pg`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.mismatch.String())
		})
	}
}
