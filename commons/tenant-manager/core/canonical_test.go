//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalTenantID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "dashed uuid is stripped",
			input: "550e8400-e29b-41d4-a716-446655440000",
			want:  "550e8400e29b41d4a716446655440000",
		},
		{
			name:  "dashless uuid is unchanged",
			input: "550e8400e29b41d4a716446655440000",
			want:  "550e8400e29b41d4a716446655440000",
		},
		{
			name:  "uppercase dashed uuid is lowercased and stripped",
			input: "550E8400-E29B-41D4-A716-446655440000",
			want:  "550e8400e29b41d4a716446655440000",
		},
		{
			name:  "uppercase dashless uuid is lowercased",
			input: "550E8400E29B41D4A716446655440000",
			want:  "550e8400e29b41d4a716446655440000",
		},
		{
			name:  "non-uuid slug passes through verbatim",
			input: "benedita",
			want:  "benedita",
		},
		{
			name:  "non-uuid slug with hyphens passes through verbatim",
			input: "tenant-123-abc",
			want:  "tenant-123-abc",
		},
		{
			name:  "non-uuid slug case is preserved",
			input: "Tenant-ABC_def",
			want:  "Tenant-ABC_def",
		},
		{
			name:    "empty is rejected",
			input:   "",
			wantErr: true,
		},
		{
			name:    "path traversal is rejected",
			input:   "tenant/../../etc",
			wantErr: true,
		},
		{
			name:    "urn uuid form is rejected",
			input:   "urn:uuid:550e8400-e29b-41d4-a716-446655440000",
			wantErr: true,
		},
		{
			name:    "braced uuid form is rejected",
			input:   "{550e8400-e29b-41d4-a716-446655440000}",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := CanonicalTenantID(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				assert.Empty(t, got)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCanonicalTenantID_IsIdempotent(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"550E8400E29B41D4A716446655440000",
		"tenant-123-abc",
	}

	for _, input := range inputs {
		first, err := CanonicalTenantID(input)
		require.NoError(t, err)

		second, err := CanonicalTenantID(first)
		require.NoError(t, err)

		assert.Equal(t, first, second, "CanonicalTenantID must be idempotent for %q", input)
	}
}

func TestCanonicalTenantID_DashedAndDashlessCollapseToSameKey(t *testing.T) {
	t.Parallel()

	dashed, err := CanonicalTenantID("550e8400-e29b-41d4-a716-446655440000")
	require.NoError(t, err)

	dashless, err := CanonicalTenantID("550e8400e29b41d4a716446655440000")
	require.NoError(t, err)

	assert.Equal(t, dashed, dashless,
		"dashed and dashless forms of the same tenant must resolve to one key namespace")
}

func TestCanonicalTenantID_OutputAlwaysPassesIsValidTenantID(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"550E8400E29B41D4A716446655440000",
		"tenant_abc",
	}

	for _, input := range inputs {
		got, err := CanonicalTenantID(input)
		require.NoError(t, err)
		assert.True(t, IsValidTenantID(got),
			"canonical form of %q must still satisfy IsValidTenantID", input)
	}
}
