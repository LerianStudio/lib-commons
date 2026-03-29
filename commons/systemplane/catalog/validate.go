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

// ValidateOption configures the behavior of ValidateKeyDefs.
type ValidateOption func(*validateConfig)

type validateConfig struct {
	ignoreFields    map[string]bool
	knownDeviations map[string]map[string]bool // key → field → true
}

// WithIgnoreFields tells ValidateKeyDefsWithOptions to skip comparison of the
// given fields for all keys. Common usage: WithIgnoreFields("EnvVar") when the
// product does not set EnvVar on its KeyDefs because values come from the
// systemplane store rather than environment variables.
func WithIgnoreFields(fields ...string) ValidateOption {
	return func(vc *validateConfig) {
		for _, field := range fields {
			vc.ignoreFields[field] = true
		}
	}
}

// WithKnownDeviation tells ValidateKeyDefsWithOptions to skip a specific
// (catalogKey, field) pair. Use this for intentional, documented deviations
// such as overriding a key's Component for product-specific ComponentDiff
// behavior.
func WithKnownDeviation(catalogKey, field string) ValidateOption {
	return func(vc *validateConfig) {
		if vc.knownDeviations[catalogKey] == nil {
			vc.knownDeviations[catalogKey] = make(map[string]bool)
		}

		vc.knownDeviations[catalogKey][field] = true
	}
}

func newValidateConfig(opts []ValidateOption) *validateConfig {
	vc := &validateConfig{
		ignoreFields:    make(map[string]bool),
		knownDeviations: make(map[string]map[string]bool),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(vc)
		}
	}

	return vc
}

func (vc *validateConfig) shouldSkip(catalogKey, field string) bool {
	if vc.ignoreFields[field] {
		return true
	}

	if fields, ok := vc.knownDeviations[catalogKey]; ok && fields[field] {
		return true
	}

	return false
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
// or AllSharedKeys(). To suppress known deviations, use
// ValidateKeyDefsWithOptions which accepts ValidateOption values.
// ValidateKeyDefs retains its original signature for backward compatibility.
func ValidateKeyDefs(productDefs []domain.KeyDef, catalogKeys ...[]SharedKey) []Mismatch {
	return ValidateKeyDefsWithOptions(productDefs, catalogKeys, nil)
}

// ValidateKeyDefsWithOptions is the full-featured variant of ValidateKeyDefs
// that accepts filtering options. Use WithIgnoreFields and WithKnownDeviation
// to suppress expected mismatches in product catalog tests.
func ValidateKeyDefsWithOptions(productDefs []domain.KeyDef, catalogKeys [][]SharedKey, opts []ValidateOption) []Mismatch {
	keyIndex, envIndex := buildCatalogIndexes(catalogKeys...)
	vc := newValidateConfig(opts)

	var mismatches []Mismatch

	for _, pd := range productDefs {
		sk, found, matchedByEnv := resolveSharedKey(pd, keyIndex, envIndex)

		if !found {
			continue // product-specific key — not in catalog, nothing to check
		}

		for _, mm := range compareKeyDef(pd, sk, matchedByEnv) {
			if vc.shouldSkip(mm.CatalogKey, mm.Field) {
				continue
			}

			mismatches = append(mismatches, mm)
		}
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
		mismatchForString(pd, sk, "RedactPolicy", string(normalizeRedactPolicy(sk.RedactPolicy, sk.Secret)), string(normalizeRedactPolicy(pd.RedactPolicy, pd.Secret))),
	}

	if matchedByEnv && pd.Key != sk.Key {
		comparisons = append(comparisons, mismatchForString(pd, sk, "Key", sk.Key, pd.Key))
	}

	if expectedEnvVars := allowedEnvVars(sk); len(expectedEnvVars) > 0 {
		if !slices.Contains(expectedEnvVars, pd.EnvVar) {
			comparisons = append(comparisons, mismatchForString(pd, sk, "EnvVar", strings.Join(expectedEnvVars, "|"), pd.EnvVar))
		}
	} else if pd.EnvVar != "" {
		// Catalog defines no env vars for this key but product sets one — flag it.
		comparisons = append(comparisons, mismatchForString(pd, sk, "EnvVar", "", pd.EnvVar))
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

func normalizeRedactPolicy(policy domain.RedactPolicy, secret bool) domain.RedactPolicy {
	if secret {
		return domain.RedactFull
	}

	if policy == "" {
		return domain.RedactNone
	}

	return policy
}
