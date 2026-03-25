// Copyright 2025 Lerian Studio.

package domain

// SnapSettingString returns a setting value coerced to string, cascading from
// tenant-scoped to global to fallback.
func SnapSettingString(snap *Snapshot, tenantID, key string, fallback string) string {
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
// tenant-scoped to global to fallback.
func SnapSettingInt(snap *Snapshot, tenantID, key string, fallback int) int {
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
// tenant-scoped to global to fallback.
func SnapSettingBool(snap *Snapshot, tenantID, key string, fallback bool) bool {
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
