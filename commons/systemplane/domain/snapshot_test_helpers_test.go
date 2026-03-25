//go:build unit

// Copyright 2025 Lerian Studio.

package domain

type stringer struct{ s string }

func (s stringer) String() string { return s.s }

type panicStringer struct{}

func (*panicStringer) String() string {
	panic("typed-nil stringer should not be invoked")
}

func snapWith(key string, val any) *Snapshot {
	return &Snapshot{
		Configs: map[string]EffectiveValue{
			key: {Key: key, Value: val},
		},
	}
}

func snapWithGlobal(key string, val any) *Snapshot {
	return &Snapshot{
		GlobalSettings: map[string]EffectiveValue{
			key: {Key: key, Value: val},
		},
	}
}

func snapWithTenant(tenantID, key string, val any) *Snapshot {
	return &Snapshot{
		TenantSettings: map[string]map[string]EffectiveValue{
			tenantID: {
				key: {Key: key, Value: val},
			},
		},
	}
}

func snapWithBoth(tenantID, key string, globalVal, tenantVal any) *Snapshot {
	return &Snapshot{
		GlobalSettings: map[string]EffectiveValue{
			key: {Key: key, Value: globalVal},
		},
		TenantSettings: map[string]map[string]EffectiveValue{
			tenantID: {
				key: {Key: key, Value: tenantVal},
			},
		},
	}
}
