package s3

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/core"
	"github.com/stretchr/testify/assert"
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

			result := GetObjectStorageKey(tt.tenantID, tt.key)

			assert.Equal(t, tt.expected, result)
		})
	}
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
				ctx = core.SetTenantIDInContext(ctx, tt.tenantID)
			}

			result := GetObjectStorageKeyForTenant(ctx, tt.key)

			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetObjectStorageKeyForTenant_NilContext(t *testing.T) {
	t.Parallel()

	result := GetObjectStorageKeyForTenant(nil, "reports/templateID/reportID.html")

	assert.Equal(t, "reports/templateID/reportID.html", result)
}

func TestGetObjectStorageKeyForTenant_UsesSameTenantID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tenantID := "org_consistency_check"

	ctx = core.SetTenantIDInContext(ctx, tenantID)

	extractedID := core.GetTenantID(ctx)
	result := GetObjectStorageKeyForTenant(ctx, "test-key")

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

			result := StripObjectStoragePrefix(tt.tenantID, tt.prefixedKey)

			assert.Equal(t, tt.expected, result)
		})
	}
}
