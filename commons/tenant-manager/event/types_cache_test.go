//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package event

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventTenantCacheInvalidate_Literal(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "tenant.cache.invalidate", EventTenantCacheInvalidate,
		"EventTenantCacheInvalidate literal must be byte-identical to tenant.cache.invalidate")
}

func TestCacheInvalidatePayload_JSONRoundTrip_SnakeCase(t *testing.T) {
	t.Parallel()

	original := CacheInvalidatePayload{
		ServiceName: testServiceName,
		Reason:      "operator-triggered",
	}

	data, err := json.Marshal(original)
	require.NoError(t, err, "marshal should succeed")

	// Field names must be snake_case.
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw), "unmarshal into map should succeed")
	assert.Contains(t, raw, "service_name", "service_name must be snake_case")
	assert.Contains(t, raw, "reason", "reason must be present when non-empty")

	var decoded CacheInvalidatePayload
	require.NoError(t, json.Unmarshal(data, &decoded), "round-trip unmarshal should succeed")
	assert.Equal(t, original, decoded, "round-trip should preserve all fields")
}

func TestCacheInvalidatePayload_OmitsEmptyReason(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(CacheInvalidatePayload{ServiceName: testServiceName})
	require.NoError(t, err, "marshal should succeed")

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw), "unmarshal into map should succeed")
	assert.Contains(t, raw, "service_name", "service_name must always be present")
	assert.NotContains(t, raw, "reason", "empty reason must be omitted via omitempty")
}
