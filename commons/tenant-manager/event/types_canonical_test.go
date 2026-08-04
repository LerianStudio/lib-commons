//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package event

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEvent_CanonicalizesTenantID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		wireID  string
		wantID  string
		wantErr bool
	}{
		{
			name:   "dashed uuid is canonicalized",
			wireID: "550e8400-e29b-41d4-a716-446655440000",
			wantID: "550e8400e29b41d4a716446655440000",
		},
		{
			name:   "dashless uuid is unchanged",
			wireID: "550e8400e29b41d4a716446655440000",
			wantID: "550e8400e29b41d4a716446655440000",
		},
		{
			name:   "uppercase uuid is lowercased",
			wireID: "550E8400-E29B-41D4-A716-446655440000",
			wantID: "550e8400e29b41d4a716446655440000",
		},
		{
			name:   "non-uuid tenant id passes through verbatim",
			wireID: "benedita",
			wantID: "benedita",
		},
		{
			name:    "malformed tenant id is rejected",
			wireID:  "tenant/../../etc",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw := []byte(`{"event_id":"evt-1","event_type":"tenant.suspended","tenant_id":"` +
				tt.wireID + `","timestamp":"2026-08-04T00:00:00Z"}`)

			evt, err := ParseEvent(raw)
			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, evt)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantID, evt.TenantID)
		})
	}
}
