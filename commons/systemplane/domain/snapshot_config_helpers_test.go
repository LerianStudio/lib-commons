//go:build unit

// Copyright 2025 Lerian Studio.

package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSnapString(t *testing.T) {
	t.Parallel()

	var nilStringer *panicStringer
	var nilInt *int
	panickingStringer := &panicStringer{}

	tests := []struct {
		name     string
		snap     *Snapshot
		key      string
		fallback string
		want     string
	}{
		{name: "nil snapshot", snap: nil, key: "k", fallback: "fb", want: "fb"},
		{name: "missing key", snap: snapWith("other", "v"), key: "k", fallback: "fb", want: "fb"},
		{name: "direct string", snap: snapWith("k", "hello"), key: "k", fallback: "fb", want: "hello"},
		{name: "empty string is not missing", snap: snapWith("k", ""), key: "k", fallback: "fb", want: ""},
		{name: "[]byte", snap: snapWith("k", []byte("bytes")), key: "k", fallback: "fb", want: "bytes"},
		{name: "fmt.Stringer", snap: snapWith("k", stringer{s: "custom"}), key: "k", fallback: "fb", want: "custom"},
		{name: "typed nil fmt.Stringer falls back", snap: snapWith("k", nilStringer), key: "k", fallback: "fb", want: "fb"},
		{name: "panicking fmt.Stringer falls back", snap: snapWith("k", panickingStringer), key: "k", fallback: "fb", want: "fb"},
		{name: "typed nil pointer falls back", snap: snapWith("k", nilInt), key: "k", fallback: "fb", want: "fb"},
		{name: "int via Sprint", snap: snapWith("k", 42), key: "k", fallback: "fb", want: "42"},
		{name: "bool via Sprint", snap: snapWith("k", true), key: "k", fallback: "fb", want: "true"},
		{name: "nil value in config", snap: snapWith("k", nil), key: "k", fallback: "fb", want: "fb"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				got := SnapString(tt.snap, tt.key, tt.fallback)
				assert.Equal(t, tt.want, got)
			})
		})
	}
}

func TestSnapInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		snap     *Snapshot
		key      string
		fallback int
		want     int
	}{
		{name: "nil snapshot", snap: nil, key: "k", fallback: -1, want: -1},
		{name: "missing key", snap: snapWith("other", 1), key: "k", fallback: -1, want: -1},
		{name: "direct int", snap: snapWith("k", 42), key: "k", fallback: -1, want: 42},
		{name: "zero int is not missing", snap: snapWith("k", 0), key: "k", fallback: -1, want: 0},
		{name: "int64", snap: snapWith("k", int64(100)), key: "k", fallback: -1, want: 100},
		{name: "float64 whole", snap: snapWith("k", float64(7)), key: "k", fallback: -1, want: 7},
		{name: "float64 fractional truncates", snap: snapWith("k", 7.9), key: "k", fallback: -1, want: 7},
		{name: "float64 negative truncates toward zero", snap: snapWith("k", -7.9), key: "k", fallback: -1, want: -7},
		{name: "string parseable", snap: snapWith("k", "99"), key: "k", fallback: -1, want: 99},
		{name: "string unparseable", snap: snapWith("k", "abc"), key: "k", fallback: -1, want: -1},
		{name: "json.Number valid", snap: snapWith("k", json.Number("123")), key: "k", fallback: -1, want: 123},
		{name: "json.Number invalid", snap: snapWith("k", json.Number("nope")), key: "k", fallback: -1, want: -1},
		{name: "bool returns fallback", snap: snapWith("k", true), key: "k", fallback: -1, want: -1},
		{name: "nil value", snap: snapWith("k", nil), key: "k", fallback: -1, want: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SnapInt(tt.snap, tt.key, tt.fallback)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSnapInt_OverflowAndSpecialFloatFallbacks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		val  any
	}{
		{name: "NaN", val: math.NaN()},
		{name: "+Inf", val: math.Inf(1)},
		{name: "-Inf", val: math.Inf(-1)},
		{name: "json.Number overflow", val: json.Number("999999999999999999999999")},
	}

	if strconv.IntSize == 32 {
		tests = append(tests, struct {
			name string
			val  any
		}{name: "int64 overflow on 32-bit", val: int64(math.MaxInt64)})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SnapInt(snapWith("k", tt.val), "k", -1)
			assert.Equal(t, -1, got)
		})
	}
}

func TestSnapBool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		snap     *Snapshot
		key      string
		fallback bool
		want     bool
	}{
		{name: "nil snapshot", snap: nil, key: "k", fallback: true, want: true},
		{name: "missing key", snap: snapWith("other", true), key: "k", fallback: true, want: true},
		{name: "direct true", snap: snapWith("k", true), key: "k", fallback: false, want: true},
		{name: "direct false is not missing", snap: snapWith("k", false), key: "k", fallback: true, want: false},
		{name: "string true", snap: snapWith("k", "true"), key: "k", fallback: false, want: true},
		{name: "string false", snap: snapWith("k", "false"), key: "k", fallback: true, want: false},
		{name: "string 1", snap: snapWith("k", "1"), key: "k", fallback: false, want: true},
		{name: "string 0", snap: snapWith("k", "0"), key: "k", fallback: true, want: false},
		{name: "string t falls back", snap: snapWith("k", "t"), key: "k", fallback: false, want: false},
		{name: "string f falls back", snap: snapWith("k", "f"), key: "k", fallback: true, want: true},
		{name: "string TRUE (case-insensitive)", snap: snapWith("k", "TRUE"), key: "k", fallback: false, want: true},
		{name: "string True (case-insensitive)", snap: snapWith("k", "True"), key: "k", fallback: false, want: true},
		{name: "string FALSE (case-insensitive)", snap: snapWith("k", "FALSE"), key: "k", fallback: true, want: false},
		{name: "string False (case-insensitive)", snap: snapWith("k", "False"), key: "k", fallback: true, want: false},
		{name: "string unparseable", snap: snapWith("k", "maybe"), key: "k", fallback: false, want: false},
		{name: "int non-zero", snap: snapWith("k", 1), key: "k", fallback: false, want: true},
		{name: "int two falls back", snap: snapWith("k", 2), key: "k", fallback: false, want: false},
		{name: "int negative falls back", snap: snapWith("k", -1), key: "k", fallback: true, want: true},
		{name: "int zero", snap: snapWith("k", 0), key: "k", fallback: true, want: false},
		{name: "int64 returns fallback", snap: snapWith("k", int64(42)), key: "k", fallback: false, want: false},
		{name: "float64 returns fallback", snap: snapWith("k", 3.14), key: "k", fallback: true, want: true},
		{name: "nil value", snap: snapWith("k", nil), key: "k", fallback: true, want: true},
		{name: "slice returns fallback", snap: snapWith("k", []string{"a"}), key: "k", fallback: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SnapBool(tt.snap, tt.key, tt.fallback)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSnapFloat64(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		snap     *Snapshot
		key      string
		fallback float64
		want     float64
	}{
		{name: "nil snapshot", snap: nil, key: "k", fallback: -1, want: -1},
		{name: "missing key", snap: snapWith("other", 1.0), key: "k", fallback: -1, want: -1},
		{name: "direct float64", snap: snapWith("k", 3.14), key: "k", fallback: -1, want: 3.14},
		{name: "zero float is not missing", snap: snapWith("k", float64(0)), key: "k", fallback: -1, want: 0},
		{name: "int", snap: snapWith("k", 7), key: "k", fallback: -1, want: 7.0},
		{name: "int64", snap: snapWith("k", int64(100)), key: "k", fallback: -1, want: 100.0},
		{name: "string parseable", snap: snapWith("k", "2.718"), key: "k", fallback: -1, want: 2.718},
		{name: "string unparseable", snap: snapWith("k", "abc"), key: "k", fallback: -1, want: -1},
		{name: "json.Number valid", snap: snapWith("k", json.Number("9.81")), key: "k", fallback: -1, want: 9.81},
		{name: "json.Number invalid", snap: snapWith("k", json.Number("bad")), key: "k", fallback: -1, want: -1},
		{name: "direct NaN falls back", snap: snapWith("k", math.NaN()), key: "k", fallback: -1, want: -1},
		{name: "direct +Inf falls back", snap: snapWith("k", math.Inf(1)), key: "k", fallback: -1, want: -1},
		{name: "direct -Inf falls back", snap: snapWith("k", math.Inf(-1)), key: "k", fallback: -1, want: -1},
		{name: "string NaN falls back", snap: snapWith("k", "NaN"), key: "k", fallback: -1, want: -1},
		{name: "string +Inf falls back", snap: snapWith("k", "+Inf"), key: "k", fallback: -1, want: -1},
		{name: "bool returns fallback", snap: snapWith("k", true), key: "k", fallback: -1, want: -1},
		{name: "nil value", snap: snapWith("k", nil), key: "k", fallback: -1, want: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SnapFloat64(tt.snap, tt.key, tt.fallback)
			assert.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

func TestSnapString_StringerInterface(t *testing.T) {
	t.Parallel()

	snap := snapWith("k", stringer{s: "via-stringer"})
	got := SnapString(snap, "k", "fb")
	assert.Equal(t, "via-stringer", got)
}

func TestSnapStringSlice_ConfigValueIsCloned(t *testing.T) {
	t.Parallel()

	original := []string{"a", "b"}
	got := SnapStringSlice(snapWith("k", original), "k", nil)
	got[0] = "changed"
	assert.Equal(t, []string{"a", "b"}, original)
}

func TestSnapConfigHelpers_FmtDescriptionsStayStable(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "7", SnapString(snapWith("k", 7), "k", "fb"))
	assert.Equal(t, fmt.Sprint(true), SnapString(snapWith("k", true), "k", "fb"))
}
