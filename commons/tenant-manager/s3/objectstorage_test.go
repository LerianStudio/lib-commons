package s3

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetObjectStorageKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tenantID string
		key      string
		expected string
	}{
		{
			name:     "prefixes key with tenant ID",
			tenantID: "org_01ABC",
			key:      "reports/templateID/reportID.html",
			expected: "org_01ABC/reports/templateID/reportID.html",
		},
		{
			name:     "returns key unchanged when tenant ID is empty",
			tenantID: "",
			key:      "reports/templateID/reportID.html",
			expected: "reports/templateID/reportID.html",
		},
		{
			name:     "handles empty key with tenant ID",
			tenantID: "org_01ABC",
			key:      "",
			expected: "org_01ABC/",
		},
		{
			name:     "handles empty key without tenant ID",
			tenantID: "",
			key:      "",
			expected: "",
		},
		{
			name:     "strips leading slash from key before prefixing",
			tenantID: "org_01ABC",
			key:      "/reports/templateID/reportID.html",
			expected: "org_01ABC/reports/templateID/reportID.html",
		},
		{
			name:     "strips leading slash from key without tenant ID",
			tenantID: "",
			key:      "/reports/templateID/reportID.html",
			expected: "reports/templateID/reportID.html",
		},
		{
			name:     "handles key with multiple leading slashes",
			tenantID: "org_01ABC",
			key:      "///reports/file.html",
			expected: "org_01ABC/reports/file.html",
		},
		{
			name:     "preserves nested path structure",
			tenantID: "tenant-456",
			key:      "a/b/c/d/file.pdf",
			expected: "tenant-456/a/b/c/d/file.pdf",
		},
		{
			name:     "handles key that is just a filename",
			tenantID: "org_01ABC",
			key:      "file.html",
			expected: "org_01ABC/file.html",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := GetObjectStorageKey(tt.tenantID, tt.key)

			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetObjectStorageKey_RejectsDelimiterInTenantID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tenantID string
	}{
		{name: "slash in middle", tenantID: "tenant/123"},
		{name: "multiple slashes", tenantID: "a/b/c"},
		{name: "slash in middle after trim", tenantID: "/tenant/123/"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := GetObjectStorageKey(tt.tenantID, "reports/file.html")

			require.Error(t, err)
			assert.Contains(t, err.Error(), "must not contain path delimiter '/'")
			assert.Empty(t, result)
		})
	}
}

func TestGetObjectStorageKey_TrimsLeadingTrailingSlashesFromTenantID(t *testing.T) {
	t.Parallel()

	// Leading/trailing slashes are trimmed, so a tenantID that is ONLY slashes
	// becomes empty and is treated as single-tenant mode.
	result, err := GetObjectStorageKey("/", "reports/file.html")

	require.NoError(t, err)
	assert.Equal(t, "reports/file.html", result)
}

func TestGetObjectStorageKeyForTenant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tenantID string
		key      string
		expected string
	}{
		{
			name:     "prefixes key with tenant ID from context",
			tenantID: "org_01ABC",
			key:      "reports/templateID/reportID.html",
			expected: "org_01ABC/reports/templateID/reportID.html",
		},
		{
			name:     "returns key unchanged when no tenant in context",
			tenantID: "",
			key:      "reports/templateID/reportID.html",
			expected: "reports/templateID/reportID.html",
		},
		{
			name:     "handles empty key with tenant in context",
			tenantID: "org_01ABC",
			key:      "",
			expected: "org_01ABC/",
		},
		{
			name:     "handles empty key without tenant in context",
			tenantID: "",
			key:      "",
			expected: "",
		},
		{
			name:     "strips leading slash from key",
			tenantID: "org_01ABC",
			key:      "/reports/templateID/reportID.html",
			expected: "org_01ABC/reports/templateID/reportID.html",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			if tt.tenantID != "" {
				ctx = core.ContextWithTenantID(ctx, tt.tenantID)
			}

			result, err := GetObjectStorageKeyForTenant(ctx, tt.key)

			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetObjectStorageKeyForTenant_NilContext(t *testing.T) {
	t.Parallel()

	result, err := GetObjectStorageKeyForTenant(nil, "reports/templateID/reportID.html")

	require.NoError(t, err)
	assert.Equal(t, "reports/templateID/reportID.html", result)
}

func TestGetObjectStorageKeyForTenant_UsesSameTenantID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tenantID := "org_consistency_check"

	ctx = core.ContextWithTenantID(ctx, tenantID)

	extractedID := core.GetTenantIDContext(ctx)

	result, err := GetObjectStorageKeyForTenant(ctx, "test-key")

	require.NoError(t, err)
	assert.Equal(t, tenantID, extractedID)
	assert.Equal(t, extractedID+"/test-key", result)
}

func TestStripObjectStoragePrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		tenantID    string
		prefixedKey string
		expected    string
	}{
		{
			name:        "strips tenant prefix from key",
			tenantID:    "org_01ABC",
			prefixedKey: "org_01ABC/reports/templateID/reportID.html",
			expected:    "reports/templateID/reportID.html",
		},
		{
			name:        "returns key unchanged when tenant ID is empty",
			tenantID:    "",
			prefixedKey: "reports/templateID/reportID.html",
			expected:    "reports/templateID/reportID.html",
		},
		{
			name:        "returns key unchanged when prefix does not match",
			tenantID:    "org_01ABC",
			prefixedKey: "other_tenant/reports/file.html",
			expected:    "other_tenant/reports/file.html",
		},
		{
			name:        "handles key that is just the prefix",
			tenantID:    "org_01ABC",
			prefixedKey: "org_01ABC/",
			expected:    "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := StripObjectStoragePrefix(tt.tenantID, tt.prefixedKey)

			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestStripObjectStoragePrefix_RejectsDelimiterInTenantID(t *testing.T) {
	t.Parallel()

	result, err := StripObjectStoragePrefix("tenant/123", "tenant/123/reports/file.html")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain path delimiter '/'")
	assert.Empty(t, result)
}
