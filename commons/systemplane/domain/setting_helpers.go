// Copyright 2025 Lerian Studio.

package domain

// SnapSettingString returns a setting value coerced to string, cascading from
// tenant-scoped to global to fallback. Nil-safe: returns fallback when snap is nil.
func SnapSettingString(snap *Snapshot, tenantID, key string, fallback string) string {
	if snap == nil {
		return fallback
	}

	if raw, ok := snap.GetTenantSetting(tenantID, key); ok {
		if value, converted := tryCoerceString(raw.Value); converted {
			return value
		}
	}

	if raw, ok := snap.GetGlobalSetting(key); ok {
		if value, converted := tryCoerceString(raw.Value); converted {
			return value
		}
	}

	return fallback
}

// SnapSettingInt returns a setting value coerced to int, cascading from
// tenant-scoped to global to fallback. Nil-safe: returns fallback when snap is nil.
func SnapSettingInt(snap *Snapshot, tenantID, key string, fallback int) int {
	if snap == nil {
		return fallback
	}

	if raw, ok := snap.GetTenantSetting(tenantID, key); ok {
		if value, converted := tryCoerceInt(raw.Value); converted {
			return value
		}
	}

	if raw, ok := snap.GetGlobalSetting(key); ok {
		if value, converted := tryCoerceInt(raw.Value); converted {
			return value
		}
	}

	return fallback
}

// SnapSettingBool returns a setting value coerced to bool, cascading from
// tenant-scoped to global to fallback. Nil-safe: returns fallback when snap is nil.
func SnapSettingBool(snap *Snapshot, tenantID, key string, fallback bool) bool {
	if snap == nil {
		return fallback
	}

	if raw, ok := snap.GetTenantSetting(tenantID, key); ok {
		if value, converted := tryCoerceBool(raw.Value); converted {
			return value
		}
	}

	if raw, ok := snap.GetGlobalSetting(key); ok {
		if value, converted := tryCoerceBool(raw.Value); converted {
			return value
		}
	}

	return fallback
}
