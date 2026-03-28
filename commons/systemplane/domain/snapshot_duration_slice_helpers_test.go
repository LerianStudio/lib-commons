//go:build unit

// Copyright 2025 Lerian Studio.

package domain

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSnapDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		snap     *Snapshot
		key      string
		fallback time.Duration
		unit     time.Duration
		want     time.Duration
	}{
		{name: "nil snapshot", snap: nil, key: "k", fallback: 5 * time.Second, unit: time.Second, want: 5 * time.Second},
		{name: "missing key", snap: snapWith("other", 1), key: "k", fallback: 5 * time.Second, unit: time.Second, want: 5 * time.Second},
		{name: "int seconds", snap: snapWith("k", 30), key: "k", fallback: 0, unit: time.Second, want: 30 * time.Second},
		{name: "int64 millis", snap: snapWith("k", int64(500)), key: "k", fallback: 0, unit: time.Millisecond, want: 500 * time.Millisecond},
		{name: "float64 seconds", snap: snapWith("k", 1.5), key: "k", fallback: 0, unit: time.Second, want: 1500 * time.Millisecond},
		{name: "zero int is not fallback", snap: snapWith("k", 0), key: "k", fallback: 5 * time.Second, unit: time.Second, want: 0},
		{name: "zero float is not fallback", snap: snapWith("k", 0.0), key: "k", fallback: 5 * time.Second, unit: time.Second, want: 0},
		{name: "string parseable duration", snap: snapWith("k", "2m30s"), key: "k", fallback: 0, unit: time.Second, want: 2*time.Minute + 30*time.Second},
		{name: "string numeric returns fallback", snap: snapWith("k", "10"), key: "k", fallback: 3 * time.Second, unit: time.Second, want: 3 * time.Second},
		{name: "string unparseable", snap: snapWith("k", "nope"), key: "k", fallback: 3 * time.Second, unit: time.Second, want: 3 * time.Second},
		{name: "nil value", snap: snapWith("k", nil), key: "k", fallback: 7 * time.Second, unit: time.Second, want: 7 * time.Second},
		{name: "bool returns fallback", snap: snapWith("k", true), key: "k", fallback: 4 * time.Second, unit: time.Second, want: 4 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SnapDuration(tt.snap, tt.key, tt.fallback, tt.unit)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSnapDuration_Float64NaNInf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value float64
	}{
		{name: "NaN", value: math.NaN()},
		{name: "+Inf", value: math.Inf(1)},
		{name: "-Inf", value: math.Inf(-1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SnapDuration(snapWith("k", tt.value), "k", 5*time.Second, time.Second)
			assert.Equal(t, 5*time.Second, got)
		})
	}
}

func TestSnapDuration_OverflowFallsBack(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		val  any
	}{
		{name: "int64 overflow", val: int64(math.MaxInt64)},
		{name: "float64 overflow", val: float64(math.MaxInt64)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SnapDuration(snapWith("k", tt.val), "k", 5*time.Second, time.Second)
			assert.Equal(t, 5*time.Second, got)
		})
	}
}

func TestSnapStringSlice(t *testing.T) {
	t.Parallel()

	fb := []string{"fallback"}

	tests := []struct {
		name     string
		snap     *Snapshot
		key      string
		fallback []string
		want     []string
	}{
		{name: "nil snapshot", snap: nil, key: "k", fallback: fb, want: fb},
		{name: "missing key", snap: snapWith("other", "x"), key: "k", fallback: fb, want: fb},
		{name: "direct []string", snap: snapWith("k", []string{"a", "b"}), key: "k", fallback: fb, want: []string{"a", "b"}},
		{name: "empty []string is not missing", snap: snapWith("k", []string{}), key: "k", fallback: fb, want: []string{}},
		{name: "typed nil []string falls back", snap: snapWith("k", []string(nil)), key: "k", fallback: fb, want: fb},
		{name: "typed nil []any falls back", snap: snapWith("k", []any(nil)), key: "k", fallback: fb, want: fb},
		{name: "[]any strings are trimmed", snap: snapWith("k", []any{"x", " y ", ""}), key: "k", fallback: fb, want: []string{"x", "y"}},
		{name: "[]any non-string falls back", snap: snapWith("k", []any{"x", 1, true}), key: "k", fallback: fb, want: fb},
		{name: "comma separated string", snap: snapWith("k", "a, b, c"), key: "k", fallback: fb, want: []string{"a", "b", "c"}},
		{name: "comma separated string filters blanks", snap: snapWith("k", "a, , b,,"), key: "k", fallback: fb, want: []string{"a", "b"}},
		{name: "empty string becomes empty slice", snap: snapWith("k", ""), key: "k", fallback: fb, want: []string{}},
		{name: "single element string", snap: snapWith("k", "only"), key: "k", fallback: fb, want: []string{"only"}},
		{name: "nil value", snap: snapWith("k", nil), key: "k", fallback: fb, want: fb},
		{name: "int returns fallback", snap: snapWith("k", 42), key: "k", fallback: fb, want: fb},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SnapStringSlice(tt.snap, tt.key, tt.fallback)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSnapStringSlice_FallbackIsCloned(t *testing.T) {
	t.Parallel()

	fallback := []string{"fallback"}
	for _, got := range [][]string{
		SnapStringSlice(nil, "k", fallback),
		SnapStringSlice(&Snapshot{}, "k", fallback),
		SnapStringSlice(snapWith("k", 42), "k", fallback),
	} {
		got[0] = "changed"
	}

	assert.Equal(t, []string{"fallback"}, fallback)
}
