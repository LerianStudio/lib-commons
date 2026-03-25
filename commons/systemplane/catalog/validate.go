// Copyright 2025 Lerian Studio.

package catalog

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
)

// Mismatch describes a divergence between a product's KeyDef and the catalog.
type Mismatch struct {
	CatalogKey   string // canonical key name
	ProductKey   string // product's key name (same if names match)
	Field        string // "Key", "EnvVar", "ApplyBehavior", "Component", "MutableAtRuntime", "ValueType", "Secret", "RedactPolicy"
	CatalogValue string
	ProductValue string
}

// String returns a human-readable description of the mismatch.
func (m Mismatch) String() string {
	if m.CatalogKey != m.ProductKey {
		return fmt.Sprintf("key %q (catalog: %q): %s: want %s, got %s",
			m.ProductKey, m.CatalogKey, m.Field, m.CatalogValue, m.ProductValue)
	}

	return fmt.Sprintf("key %q: %s: want %s, got %s",
		m.CatalogKey, m.Field, m.CatalogValue, m.ProductValue)
}

// ValidateKeyDefs checks product KeyDefs against the canonical catalog.
// Matching is by exact Key name first, then by EnvVar or MatchEnvVars when the
// product key name differs but still points to the same canonical configuration
// concept.
// For each match, compares Key, EnvVar, ApplyBehavior, Component,
// MutableAtRuntime, ValueType, Secret, and RedactPolicy.
//
// Does NOT flag keys that exist in the product but not in the catalog
// (those are product-specific keys, which is fine).
//
// catalogKeys accepts variadic slices so callers can pass individual categories
// or AllSharedKeys().
func ValidateKeyDefs(productDefs []domain.KeyDef, catalogKeys ...[]SharedKey) []Mismatch {
	keyIndex, envIndex := buildCatalogIndexes(catalogKeys...)

	var mismatches []Mismatch

	for _, pd := range productDefs {
		sk, found, matchedByEnv := resolveSharedKey(pd, keyIndex, envIndex)

		if !found {
			continue // product-specific key — not in catalog, nothing to check
		}

		mismatches = append(mismatches, compareKeyDef(pd, sk, matchedByEnv)...)
	}

	sort.Slice(mismatches, func(i, j int) bool {
		if mismatches[i].CatalogKey != mismatches[j].CatalogKey {
			return mismatches[i].CatalogKey < mismatches[j].CatalogKey
		}

		return mismatches[i].Field < mismatches[j].Field
	})

	return mismatches
}

func buildCatalogIndexes(catalogKeys ...[]SharedKey) (map[string]SharedKey, map[string]SharedKey) {
	keyIndex := make(map[string]SharedKey)
	envIndex := make(map[string]SharedKey)

	for _, slice := range catalogKeys {
		for _, sk := range slice {
			keyIndex[sk.Key] = sk
			for _, envVar := range allowedEnvVars(sk) {
				envIndex[envVar] = sk
			}
		}
	}

	return keyIndex, envIndex
}

func resolveSharedKey(pd domain.KeyDef, keyIndex map[string]SharedKey, envIndex map[string]SharedKey) (SharedKey, bool, bool) {
	if sk, found := keyIndex[pd.Key]; found {
		return sk, true, false
	}

	if pd.EnvVar == "" {
		return SharedKey{}, false, false
	}

	sk, found := envIndex[pd.EnvVar]

	return sk, found, found
}

func compareKeyDef(pd domain.KeyDef, sk SharedKey, matchedByEnv bool) []Mismatch {
	comparisons := []Mismatch{
		mismatchForString(pd, sk, "ApplyBehavior", string(sk.ApplyBehavior), string(pd.ApplyBehavior)),
		mismatchForString(pd, sk, "Component", sk.Component, pd.Component),
		mismatchForString(pd, sk, "MutableAtRuntime", strconv.FormatBool(sk.MutableAtRuntime), strconv.FormatBool(pd.MutableAtRuntime)),
		mismatchForString(pd, sk, "ValueType", string(sk.ValueType), string(pd.ValueType)),
		mismatchForString(pd, sk, "Secret", strconv.FormatBool(sk.Secret), strconv.FormatBool(pd.Secret)),
		mismatchForString(pd, sk, "RedactPolicy", string(sk.RedactPolicy), string(pd.RedactPolicy)),
	}

	if matchedByEnv && pd.Key != sk.Key {
		comparisons = append(comparisons, mismatchForString(pd, sk, "Key", sk.Key, pd.Key))
	}

	if expectedEnvVars := allowedEnvVars(sk); len(expectedEnvVars) > 0 && !containsString(expectedEnvVars, pd.EnvVar) {
		comparisons = append(comparisons, mismatchForString(pd, sk, "EnvVar", strings.Join(expectedEnvVars, "|"), pd.EnvVar))
	}

	mismatches := make([]Mismatch, 0, len(comparisons))
	for _, comparison := range comparisons {
		if comparison.Field == "" {
			continue
		}

		mismatches = append(mismatches, comparison)
	}

	return mismatches
}

func mismatchForString(pd domain.KeyDef, sk SharedKey, field, catalogValue, productValue string) Mismatch {
	if catalogValue == productValue {
		return Mismatch{}
	}

	return Mismatch{
		CatalogKey:   sk.Key,
		ProductKey:   pd.Key,
		Field:        field,
		CatalogValue: catalogValue,
		ProductValue: productValue,
	}
}

func allowedEnvVars(sk SharedKey) []string {
	allowed := make([]string, 0, 1+len(sk.MatchEnvVars))
	if sk.EnvVar != "" {
		allowed = append(allowed, sk.EnvVar)
	}

	allowed = append(allowed, sk.MatchEnvVars...)

	return allowed
}

func containsString(values []string, target string) bool {
	return slices.Contains(values, target)
}
