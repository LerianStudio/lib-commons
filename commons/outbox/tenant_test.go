//go:build unit

package outbox

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContextWithTenantID_TrimsWhitespace(t *testing.T) {
	t.Parallel()

	// IDs with leading/trailing spaces are now trimmed before storing.
	ctx := ContextWithTenantID(nil, "  tenant-1  ")
	tenantID, ok := TenantIDFromContext(ctx)

	require.True(t, ok)
	require.Equal(t, "tenant-1", tenantID)
}

func TestContextWithTenantIDStrict_ReturnsErrorOnWhitespace(t *testing.T) {
	t.Parallel()

	ctx, err := ContextWithTenantIDStrict(context.Background(), "  tenant-1  ")
	require.ErrorIs(t, err, ErrTenantIDWhitespace)

	// Trimmed value is still usable
	tenantID, ok := TenantIDFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "tenant-1", tenantID)
}

func TestContextWithTenantIDStrict_NoErrorOnCleanID(t *testing.T) {
	t.Parallel()

	ctx, err := ContextWithTenantIDStrict(context.Background(), "tenant-1")
	require.NoError(t, err)

	tenantID, ok := TenantIDFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "tenant-1", tenantID)
}

func TestContextWithTenantID_NilContextUsesBackground(t *testing.T) {
	t.Parallel()

	ctx := ContextWithTenantID(nil, "tenant-1")
	tenantID, ok := TenantIDFromContext(ctx)

	require.True(t, ok)
	require.Equal(t, "tenant-1", tenantID)
}

func TestTenantIDFromContext_RoundTrip(t *testing.T) {
	t.Parallel()

	ctx := ContextWithTenantID(context.Background(), "tenant-42")
	tenantID, ok := TenantIDFromContext(ctx)

	require.True(t, ok)
	require.Equal(t, "tenant-42", tenantID)
}

func TestTenantIDFromContext_InvalidCases(t *testing.T) {
	t.Parallel()

	tenantID, ok := TenantIDFromContext(nil)
	require.False(t, ok)
	require.Empty(t, tenantID)

	ctx := ContextWithTenantID(context.Background(), "   ")
	tenantID, ok = TenantIDFromContext(ctx)
	require.False(t, ok)
	require.Empty(t, tenantID)
}

func TestTenantIDFromContext_TrimsStoredWhitespace(t *testing.T) {
	t.Parallel()

	// Even if whitespace somehow got into the context, TenantIDFromContext trims it.
	ctx := context.WithValue(context.Background(), TenantIDContextKey, "  spaced  ")
	tenantID, ok := TenantIDFromContext(ctx)

	require.True(t, ok)
	require.Equal(t, "spaced", tenantID)
}
