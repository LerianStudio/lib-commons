package poolmanager

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPackage_Exists(t *testing.T) {
	// This test verifies that the tenants package exists and can be imported.
	// The package should provide multi-tenant context and error handling utilities.

	// Verify package-level constants are defined
	t.Run("package constants should be defined", func(t *testing.T) {
		// TenantIDContextKey should be exported (typed ContextKey)
		assert.NotEmpty(t, TenantIDContextKey, "TenantIDContextKey should be defined and non-empty")
	})
}

func TestPackage_Documentation(t *testing.T) {
	// Verify the package has proper documentation via doc.go

	t.Run("package should have documentation", func(t *testing.T) {
		// PackageName should be defined in doc.go
		assert.Equal(t, "tenants", PackageName, "PackageName should be 'tenants'")
	})
}
