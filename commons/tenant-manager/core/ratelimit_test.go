// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

//go:build unit

package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRateLimitSettings_ForTier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		settings  RateLimitSettings
		tierName  string
		wantLimit *TierLimit
		wantOK    bool
	}{
		{
			name:      "nil settings",
			settings:  nil,
			tierName:  "default",
			wantLimit: nil,
			wantOK:    false,
		},
		{
			name:      "empty map",
			settings:  RateLimitSettings{},
			tierName:  "default",
			wantLimit: nil,
			wantOK:    false,
		},
		{
			name: "key not found",
			settings: RateLimitSettings{
				"aggressive": &TierLimit{Max: 10, Window: 10},
			},
			tierName:  "default",
			wantLimit: nil,
			wantOK:    false,
		},
		{
			name: "key found but nil value",
			settings: RateLimitSettings{
				"default": nil,
			},
			tierName:  "default",
			wantLimit: nil,
			wantOK:    false,
		},
		{
			name: "key found but Max is zero",
			settings: RateLimitSettings{
				"default": &TierLimit{Max: 0, Window: 60},
			},
			tierName:  "default",
			wantLimit: nil,
			wantOK:    false,
		},
		{
			name: "key found but Window is zero",
			settings: RateLimitSettings{
				"default": &TierLimit{Max: 50, Window: 0},
			},
			tierName:  "default",
			wantLimit: nil,
			wantOK:    false,
		},
		{
			name: "key found but Max is negative",
			settings: RateLimitSettings{
				"default": &TierLimit{Max: -1, Window: 60},
			},
			tierName:  "default",
			wantLimit: nil,
			wantOK:    false,
		},
		{
			name: "key found but Window is negative",
			settings: RateLimitSettings{
				"default": &TierLimit{Max: 50, Window: -10},
			},
			tierName:  "default",
			wantLimit: nil,
			wantOK:    false,
		},
		{
			name: "key found with valid Max and Window",
			settings: RateLimitSettings{
				"default": &TierLimit{Max: 50, Window: 60},
			},
			tierName:  "default",
			wantLimit: &TierLimit{Max: 50, Window: 60},
			wantOK:    true,
		},
		{
			name: "custom tier name",
			settings: RateLimitSettings{
				"export":  &TierLimit{Max: 5, Window: 3600},
				"default": &TierLimit{Max: 100, Window: 60},
			},
			tierName:  "export",
			wantLimit: &TierLimit{Max: 5, Window: 3600},
			wantOK:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := tc.settings.ForTier(tc.tierName)

			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantLimit, got)
		})
	}
}
