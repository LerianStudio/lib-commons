// Copyright 2025 Lerian Studio.

package ports

import (
	"context"
	"strings"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
)

// AllowAllAuthorizer permits every operation unconditionally.
// Use only when authentication is explicitly disabled.
type AllowAllAuthorizer struct{}

// Compile-time interface check.
var _ Authorizer = (*AllowAllAuthorizer)(nil)

func (a *AllowAllAuthorizer) Authorize(ctx context.Context, permission string) error {
	if domain.IsNilValue(a) {
		return domain.ErrPermissionDenied
	}

	if ctx == nil || strings.TrimSpace(permission) == "" {
		return domain.ErrPermissionDenied
	}

	return nil
}

// PermissionCheckerFunc is a callback that checks whether the current actor
// has access to the given resource and action. Implementations typically
// delegate to an external auth service (e.g., lib-auth).
type PermissionCheckerFunc func(ctx context.Context, resource, action string) error

// DelegatingAuthorizer splits permission strings and forwards each check
// to an external auth service via the CheckPermission callback.
//
// Permission format: "resource<separator>action" (e.g., "system/configs:read").
// Default separator is ":".
type DelegatingAuthorizer struct {
	// CheckPermission delegates to the external auth service.
	// MUST NOT be nil — if nil, Authorize returns ErrPermissionDenied (fail-closed).
	CheckPermission PermissionCheckerFunc

	// Separator splits the permission string into resource and action.
	// Defaults to ":" if empty.
	Separator string
}

// Compile-time interface check.
var _ Authorizer = (*DelegatingAuthorizer)(nil)

func (a *DelegatingAuthorizer) Authorize(ctx context.Context, permission string) error {
	if domain.IsNilValue(a) || a.CheckPermission == nil {
		return domain.ErrPermissionDenied
	}

	if ctx == nil {
		return domain.ErrPermissionDenied
	}

	sep := a.Separator
	if sep == "" {
		sep = ":"
	}

	permission = strings.TrimSpace(permission)
	if permission == "" {
		return domain.ErrPermissionDenied
	}

	resource, action := splitPermission(permission, sep)
	resource = strings.TrimSpace(resource)

	action = strings.TrimSpace(action)
	if resource == "" || (strings.Contains(permission, sep) && action == "") {
		return domain.ErrPermissionDenied
	}

	return a.CheckPermission(ctx, resource, action)
}

// splitPermission splits a permission string by the last occurrence of sep.
// "system/configs:read" with sep ":" -> ("system/configs", "read")
// "admin" with no sep found -> ("admin", "")
func splitPermission(permission, sep string) (resource, action string) {
	idx := strings.LastIndex(permission, sep)
	if idx < 0 {
		return permission, ""
	}

	return permission[:idx], permission[idx+len(sep):]
}
