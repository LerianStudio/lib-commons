//go:build unit

// Copyright 2025 Lerian Studio.

package catalog

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateKeyDefsWithOptions_WithIgnoreFields(t *testing.T) {
	t.Parallel()

	productDefs := []domain.KeyDef{
		{
			Key: "app.log_level", ValueType: domain.ValueTypeString,
			ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true,
			Component: domain.ComponentNone,
			// EnvVar intentionally empty — would normally mismatch the catalog's "LOG_LEVEL".
		},
	}

	// Without options: should produce an EnvVar mismatch.
	raw := ValidateKeyDefs(productDefs, AppServerKeys())
	envVarMismatches := filterByField(raw, "EnvVar")
	require.NotEmpty(t, envVarMismatches, "expected EnvVar mismatch without options")

	// With WithIgnoreFields("EnvVar"): should suppress the EnvVar mismatch.
	filtered := ValidateKeyDefsWithOptions(productDefs, [][]SharedKey{AppServerKeys()},
		[]ValidateOption{WithIgnoreFields("EnvVar")})
	envVarMismatches = filterByField(filtered, "EnvVar")
	assert.Empty(t, envVarMismatches, "EnvVar mismatches should be suppressed by WithIgnoreFields")
}

func TestValidateKeyDefsWithOptions_WithKnownDeviation(t *testing.T) {
	t.Parallel()

	productDefs := []domain.KeyDef{
		{
			Key: "app.log_level", ValueType: domain.ValueTypeString,
			ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true,
			Component: "logger", // intentional deviation from catalog's ComponentNone
			EnvVar:    "LOG_LEVEL",
		},
	}

	// Without options: should produce a Component mismatch.
	raw := ValidateKeyDefs(productDefs, AppServerKeys())
	componentMismatches := filterByField(raw, "Component")
	require.NotEmpty(t, componentMismatches, "expected Component mismatch without options")

	// With WithKnownDeviation: should suppress only that key+field.
	filtered := ValidateKeyDefsWithOptions(productDefs, [][]SharedKey{AppServerKeys()},
		[]ValidateOption{WithKnownDeviation("app.log_level", "Component")})
	componentMismatches = filterByField(filtered, "Component")
	assert.Empty(t, componentMismatches, "Component mismatch should be suppressed by WithKnownDeviation")
}

func TestValidateKeyDefsWithOptions_KnownDeviationDoesNotSuppressOtherKeys(t *testing.T) {
	t.Parallel()

	productDefs := []domain.KeyDef{
		{
			Key: "app.log_level", ValueType: domain.ValueTypeString,
			ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true,
			Component: "logger", EnvVar: "LOG_LEVEL",
		},
		{
			Key: "cors.allowed_origins", ValueType: domain.ValueTypeString,
			ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true,
			Component: "wrong-component", EnvVar: "CORS_ALLOWED_ORIGINS",
		},
	}

	filtered := ValidateKeyDefsWithOptions(productDefs,
		[][]SharedKey{AppServerKeys(), CORSKeys()},
		[]ValidateOption{WithKnownDeviation("app.log_level", "Component")})

	// app.log_level Component should be suppressed.
	for _, mm := range filtered {
		if mm.CatalogKey == "app.log_level" && mm.Field == "Component" {
			t.Error("app.log_level Component deviation should have been suppressed")
		}
	}

	// cors.allowed_origins Component should NOT be suppressed.
	corsComponentFound := false
	for _, mm := range filtered {
		if mm.CatalogKey == "cors.allowed_origins" && mm.Field == "Component" {
			corsComponentFound = true
		}
	}

	assert.True(t, corsComponentFound, "cors.allowed_origins Component mismatch should NOT be suppressed")
}

func TestValidateKeyDefsWithOptions_NilOptions(t *testing.T) {
	t.Parallel()

	productDefs := []domain.KeyDef{
		{
			Key: "app.log_level", ValueType: domain.ValueTypeString,
			ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true,
			Component: domain.ComponentNone, EnvVar: "LOG_LEVEL",
		},
	}

	// Nil options should work the same as no options.
	result := ValidateKeyDefsWithOptions(productDefs, [][]SharedKey{AppServerKeys()}, nil)
	assert.Empty(t, result, "matching key with nil options should produce no mismatches")
}

func TestValidateKeyDefsWithOptions_CombinedOptions(t *testing.T) {
	t.Parallel()

	productDefs := []domain.KeyDef{
		{
			Key: "app.log_level", ValueType: domain.ValueTypeString,
			ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true,
			Component: "logger",
			// EnvVar empty — both Component and EnvVar would mismatch.
		},
	}

	filtered := ValidateKeyDefsWithOptions(productDefs, [][]SharedKey{AppServerKeys()},
		[]ValidateOption{
			WithIgnoreFields("EnvVar"),
			WithKnownDeviation("app.log_level", "Component"),
		})

	assert.Empty(t, filtered, "all mismatches should be suppressed by combined options")
}

func filterByField(mismatches []Mismatch, field string) []Mismatch {
	var result []Mismatch

	for _, mm := range mismatches {
		if mm.Field == field {
			result = append(result, mm)
		}
	}

	return result
}
