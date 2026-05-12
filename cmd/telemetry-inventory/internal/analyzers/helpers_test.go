//go:build unit

package analyzers

import (
	"testing"
)

// TestUniqueSorted pins the dedup-and-sort contract used widely across the
// analyzer pipeline. Pure-function helper — no AST dependency.
func TestUniqueSorted(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil input", nil, nil},
		{"empty input", []string{}, nil},
		{"single element", []string{"a"}, []string{"a"}},
		{"already sorted", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"unsorted", []string{"c", "a", "b"}, []string{"a", "b", "c"}},
		{"duplicates", []string{"b", "a", "b", "a"}, []string{"a", "b"}},
		{"empty strings dropped", []string{"a", "", "b", ""}, []string{"a", "b"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := uniqueSorted(tc.in)
			if !stringSlicesEqual(got, tc.want) {
				t.Fatalf("uniqueSorted(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestMergeStrings exercises the additive merge variant of uniqueSorted.
// Pure-function helper.
func TestMergeStrings(t *testing.T) {
	cases := []struct {
		name     string
		existing []string
		next     []string
		want     []string
	}{
		{"both nil", nil, nil, nil},
		{"existing only", []string{"a", "b"}, nil, []string{"a", "b"}},
		{"next only", nil, []string{"a", "b"}, []string{"a", "b"}},
		{"overlap", []string{"a", "b"}, []string{"b", "c"}, []string{"a", "b", "c"}},
		{"disjoint", []string{"a"}, []string{"b"}, []string{"a", "b"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeStrings(tc.existing, tc.next...)
			if !stringSlicesEqual(got, tc.want) {
				t.Fatalf("mergeStrings(%v, %v) = %v, want %v", tc.existing, tc.next, got, tc.want)
			}
		})
	}
}

// TestContainsString exercises the membership-test helper.
func TestContainsString(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		needle string
		want   bool
	}{
		{"nil slice", nil, "a", false},
		{"empty slice", []string{}, "a", false},
		{"present", []string{"a", "b", "c"}, "b", true},
		{"absent", []string{"a", "b", "c"}, "z", false},
		{"empty needle in empty slice", []string{}, "", false},
		{"empty needle in slice with empty", []string{""}, "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := containsString(tc.values, tc.needle)
			if got != tc.want {
				t.Fatalf("containsString(%v, %q) = %t, want %t", tc.values, tc.needle, got, tc.want)
			}
		})
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
