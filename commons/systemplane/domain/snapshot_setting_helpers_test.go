//go:build unit

// Copyright 2025 Lerian Studio.

package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSnapSettingString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		snap     *Snapshot
		tenant   string
		key      string
		fallback string
		want     string
	}{
		{name: "nil snapshot", snap: nil, tenant: "t1", key: "k", fallback: "fb", want: "fb"},
		{name: "tenant value wins", snap: snapWithBoth("t1", "k", "global", "tenant"), tenant: "t1", key: "k", fallback: "fb", want: "tenant"},
		{name: "falls through to global", snap: snapWithGlobal("k", "global"), tenant: "t1", key: "k", fallback: "fb", want: "global"},
		{name: "falls through to fallback", snap: &Snapshot{}, tenant: "t1", key: "k", fallback: "fb", want: "fb"},
		{name: "tenant value with type coercion", snap: snapWithTenant("t1", "k", 42), tenant: "t1", key: "k", fallback: "fb", want: "42"},
		{name: "tenant empty string overrides global", snap: snapWithBoth("t1", "k", "global", ""), tenant: "t1", key: "k", fallback: "fb", want: ""},
		{name: "tenant nil falls through to global", snap: snapWithBoth("t1", "k", "global", nil), tenant: "t1", key: "k", fallback: "fb", want: "global"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SnapSettingString(tt.snap, tt.tenant, tt.key, tt.fallback)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSnapSettingInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		snap     *Snapshot
		tenant   string
		key      string
		fallback int
		want     int
	}{
		{name: "nil snapshot", snap: nil, tenant: "t1", key: "k", fallback: -1, want: -1},
		{name: "tenant value wins", snap: snapWithBoth("t1", "k", 10, 20), tenant: "t1", key: "k", fallback: -1, want: 20},
		{name: "falls through to global", snap: snapWithGlobal("k", 10), tenant: "t1", key: "k", fallback: -1, want: 10},
		{name: "falls through to fallback", snap: &Snapshot{}, tenant: "t1", key: "k", fallback: -1, want: -1},
		{name: "global with string coercion", snap: snapWithGlobal("k", "77"), tenant: "t1", key: "k", fallback: -1, want: 77},
		{name: "tenant nil falls through to global", snap: snapWithBoth("t1", "k", 7, nil), tenant: "t1", key: "k", fallback: -1, want: 7},
		{name: "tenant malformed falls through to global", snap: snapWithBoth("t1", "k", 7, []string{"bad"}), tenant: "t1", key: "k", fallback: -1, want: 7},
		{name: "global malformed falls through to fallback", snap: snapWithGlobal("k", []string{"bad"}), tenant: "t1", key: "k", fallback: -1, want: -1},
		{name: "tenant zero overrides global", snap: snapWithBoth("t1", "k", 7, 0), tenant: "t1", key: "k", fallback: -1, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SnapSettingInt(tt.snap, tt.tenant, tt.key, tt.fallback)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSnapSettingBool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		snap     *Snapshot
		tenant   string
		key      string
		fallback bool
		want     bool
	}{
		{name: "nil snapshot", snap: nil, tenant: "t1", key: "k", fallback: false, want: false},
		{name: "tenant value wins", snap: snapWithBoth("t1", "k", false, true), tenant: "t1", key: "k", fallback: false, want: true},
		{name: "falls through to global", snap: snapWithGlobal("k", true), tenant: "t1", key: "k", fallback: false, want: true},
		{name: "falls through to fallback", snap: &Snapshot{}, tenant: "t1", key: "k", fallback: true, want: true},
		{name: "tenant string coercion", snap: snapWithTenant("t1", "k", "true"), tenant: "t1", key: "k", fallback: false, want: true},
		{name: "tenant invalid bool string falls through to global", snap: snapWithBoth("t1", "k", true, "t"), tenant: "t1", key: "k", fallback: false, want: true},
		{name: "tenant int coercion zero", snap: snapWithTenant("t1", "k", 0), tenant: "t1", key: "k", fallback: true, want: false},
		{name: "tenant invalid int falls through to global", snap: snapWithBoth("t1", "k", true, 2), tenant: "t1", key: "k", fallback: false, want: true},
		{name: "tenant nil falls through to global", snap: snapWithBoth("t1", "k", true, nil), tenant: "t1", key: "k", fallback: false, want: true},
		{name: "tenant malformed falls through to global", snap: snapWithBoth("t1", "k", true, []string{"bad"}), tenant: "t1", key: "k", fallback: false, want: true},
		{name: "global malformed falls through to fallback", snap: snapWithGlobal("k", []string{"bad"}), tenant: "t1", key: "k", fallback: false, want: false},
		{name: "tenant false overrides global true", snap: snapWithBoth("t1", "k", true, false), tenant: "t1", key: "k", fallback: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SnapSettingBool(tt.snap, tt.tenant, tt.key, tt.fallback)
			assert.Equal(t, tt.want, got)
		})
	}
}
