// Copyright 2025 Lerian Studio.

package domain

import "time"

// SnapString returns the config value for key coerced to a string,
// or fallback when snap is nil, the key is absent, or coercion fails.
func SnapString(snap *Snapshot, key string, fallback string) string {
	return coerceString(snap.ConfigValue(key, nil), fallback)
}

// SnapInt returns the config value for key coerced to an int,
// or fallback when snap is nil, the key is absent, or coercion fails.
func SnapInt(snap *Snapshot, key string, fallback int) int {
	return coerceInt(snap.ConfigValue(key, nil), fallback)
}

// SnapBool returns the config value for key coerced to a bool,
// or fallback when snap is nil, the key is absent, or coercion fails.
func SnapBool(snap *Snapshot, key string, fallback bool) bool {
	return coerceBool(snap.ConfigValue(key, nil), fallback)
}

// SnapFloat64 returns the config value for key coerced to a float64,
// or fallback when snap is nil, the key is absent, or coercion fails.
func SnapFloat64(snap *Snapshot, key string, fallback float64) float64 {
	return coerceFloat64(snap.ConfigValue(key, nil), fallback)
}

// SnapDuration returns the config value for key coerced to a time.Duration.
// Numeric values are multiplied by unit (e.g. time.Second). String values must
// be parseable by time.ParseDuration. Returns fallback when snap is nil, the
// key is absent, or coercion fails.
func SnapDuration(snap *Snapshot, key string, fallback time.Duration, unit time.Duration) time.Duration {
	return coerceDuration(snap.ConfigValue(key, nil), fallback, unit)
}

// SnapStringSlice returns the config value for key coerced to a []string,
// or fallback when snap is nil, the key is absent, or coercion fails.
func SnapStringSlice(snap *Snapshot, key string, fallback []string) []string {
	return coerceStringSlice(snap.ConfigValue(key, nil), fallback)
}
