//go:build unit

// Copyright 2025 Lerian Studio.

package ports

import (
	"context"
	"errors"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllowAllAuthorizer_AlwaysNil(t *testing.T) {
	t.Parallel()

	auth := &AllowAllAuthorizer{}

	perms := []string{
		"system/configs:read",
		"admin:delete",
		"anything-at-all",
		"",
	}

	for _, perm := range perms {
		err := auth.Authorize(context.Background(), perm)
		assert.NoError(t, err, "permission %q should be allowed", perm)
	}
}

func TestDelegatingAuthorizer_SplitsCorrectly(t *testing.T) {
	t.Parallel()

	var gotResource, gotAction string

	auth := &DelegatingAuthorizer{
		CheckPermission: func(_ context.Context, resource, action string) error {
			gotResource = resource
			gotAction = action

			return nil
		},
	}

	err := auth.Authorize(context.Background(), "system/configs:read")

	require.NoError(t, err)
	assert.Equal(t, "system/configs", gotResource)
	assert.Equal(t, "read", gotAction)
}

func TestDelegatingAuthorizer_CustomSeparator(t *testing.T) {
	t.Parallel()

	var gotResource, gotAction string

	auth := &DelegatingAuthorizer{
		CheckPermission: func(_ context.Context, resource, action string) error {
			gotResource = resource
			gotAction = action

			return nil
		},
		Separator: ".",
	}

	err := auth.Authorize(context.Background(), "system.write")

	require.NoError(t, err)
	assert.Equal(t, "system", gotResource)
	assert.Equal(t, "write", gotAction)
}

func TestDelegatingAuthorizer_NoSeparatorInPermission(t *testing.T) {
	t.Parallel()

	var gotResource, gotAction string

	auth := &DelegatingAuthorizer{
		CheckPermission: func(_ context.Context, resource, action string) error {
			gotResource = resource
			gotAction = action

			return nil
		},
	}

	err := auth.Authorize(context.Background(), "admin")

	require.NoError(t, err)
	assert.Equal(t, "admin", gotResource)
	assert.Equal(t, "", gotAction)
}

func TestDelegatingAuthorizer_WhitespaceTrimmed(t *testing.T) {
	t.Parallel()

	var gotResource, gotAction string

	auth := &DelegatingAuthorizer{
		CheckPermission: func(_ context.Context, resource, action string) error {
			gotResource = resource
			gotAction = action

			return nil
		},
	}

	err := auth.Authorize(context.Background(), "  system/configs : read  ")

	require.NoError(t, err)
	assert.Equal(t, "system/configs", gotResource)
	assert.Equal(t, "read", gotAction)
}

func TestDelegatingAuthorizer_ExplicitlyMalformedPermission_FailsClosed(t *testing.T) {
	t.Parallel()

	auth := &DelegatingAuthorizer{
		CheckPermission: func(_ context.Context, _, _ string) error {
			return nil
		},
	}

	assert.ErrorIs(t, auth.Authorize(context.Background(), ":"), domain.ErrPermissionDenied)
	assert.ErrorIs(t, auth.Authorize(context.Background(), "configs:"), domain.ErrPermissionDenied)
	assert.ErrorIs(t, auth.Authorize(context.Background(), ":read"), domain.ErrPermissionDenied)
	assert.ErrorIs(t, auth.Authorize(context.Background(), "   "), domain.ErrPermissionDenied)
	assert.ErrorIs(t, auth.Authorize(context.Background(), "configs:   "), domain.ErrPermissionDenied)
	assert.ErrorIs(t, auth.Authorize(context.Background(), "   :read"), domain.ErrPermissionDenied)
}

func TestDelegatingAuthorizer_TypedNilReceiver_FailsClosed(t *testing.T) {
	t.Parallel()

	var auth *DelegatingAuthorizer
	err := auth.Authorize(context.Background(), "configs:read")
	assert.ErrorIs(t, err, domain.ErrPermissionDenied)
}

func TestDelegatingAuthorizer_NilChecker_FailsClosed(t *testing.T) {
	t.Parallel()

	auth := &DelegatingAuthorizer{
		CheckPermission: nil,
	}

	err := auth.Authorize(context.Background(), "anything:read")

	require.ErrorIs(t, err, domain.ErrPermissionDenied)
}

func TestDelegatingAuthorizer_NilContext_FailsClosed(t *testing.T) {
	t.Parallel()

	auth := &DelegatingAuthorizer{
		CheckPermission: func(_ context.Context, _, _ string) error {
			return nil
		},
	}

	err := auth.Authorize(nil, "anything:read")

	require.ErrorIs(t, err, domain.ErrPermissionDenied)
}

func TestDelegatingAuthorizer_PropagatesError(t *testing.T) {
	t.Parallel()

	customErr := errors.New("external auth service unavailable")

	auth := &DelegatingAuthorizer{
		CheckPermission: func(_ context.Context, _, _ string) error {
			return customErr
		},
	}

	err := auth.Authorize(context.Background(), "system/configs:read")

	require.ErrorIs(t, err, customErr)
}

func TestDelegatingAuthorizer_CheckerSuccess(t *testing.T) {
	t.Parallel()

	auth := &DelegatingAuthorizer{
		CheckPermission: func(_ context.Context, _, _ string) error {
			return nil
		},
	}

	err := auth.Authorize(context.Background(), "system/configs:write")

	require.NoError(t, err)
}

func TestDelegatingAuthorizer_DefaultSeparator(t *testing.T) {
	t.Parallel()

	var gotResource, gotAction string

	auth := &DelegatingAuthorizer{
		CheckPermission: func(_ context.Context, resource, action string) error {
			gotResource = resource
			gotAction = action

			return nil
		},
		Separator: "", // should default to ":"
	}

	err := auth.Authorize(context.Background(), "configs:delete")

	require.NoError(t, err)
	assert.Equal(t, "configs", gotResource)
	assert.Equal(t, "delete", gotAction)
}

func TestDelegatingAuthorizer_MultipleSeparators(t *testing.T) {
	t.Parallel()

	var gotResource, gotAction string

	auth := &DelegatingAuthorizer{
		CheckPermission: func(_ context.Context, resource, action string) error {
			gotResource = resource
			gotAction = action

			return nil
		},
	}

	// Contains two ":"; should split on the LAST one.
	err := auth.Authorize(context.Background(), "system/configs/schema:read")

	require.NoError(t, err)
	assert.Equal(t, "system/configs/schema", gotResource)
	assert.Equal(t, "read", gotAction)
}

func TestSplitPermission(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		permission   string
		sep          string
		wantResource string
		wantAction   string
	}{
		{
			name:         "standard colon split",
			permission:   "configs:read",
			sep:          ":",
			wantResource: "configs",
			wantAction:   "read",
		},
		{
			name:         "nested resource with colon",
			permission:   "system/configs:write",
			sep:          ":",
			wantResource: "system/configs",
			wantAction:   "write",
		},
		{
			name:         "multiple colons splits on last",
			permission:   "a:b:c",
			sep:          ":",
			wantResource: "a:b",
			wantAction:   "c",
		},
		{
			name:         "no separator found",
			permission:   "admin",
			sep:          ":",
			wantResource: "admin",
			wantAction:   "",
		},
		{
			name:         "dot separator",
			permission:   "system.delete",
			sep:          ".",
			wantResource: "system",
			wantAction:   "delete",
		},
		{
			name:         "multi-char separator",
			permission:   "system::read",
			sep:          "::",
			wantResource: "system",
			wantAction:   "read",
		},
		{
			name:         "empty permission",
			permission:   "",
			sep:          ":",
			wantResource: "",
			wantAction:   "",
		},
		{
			name:         "separator only",
			permission:   ":",
			sep:          ":",
			wantResource: "",
			wantAction:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resource, action := splitPermission(tt.permission, tt.sep)

			assert.Equal(t, tt.wantResource, resource)
			assert.Equal(t, tt.wantAction, action)
		})
	}
}
