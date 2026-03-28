// Copyright 2025 Lerian Studio.

package ports

import (
	"context"
	"strings"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
)

// FuncIdentityResolver adapts simple extraction functions to the IdentityResolver
// interface. Products wire their existing context-extraction logic (e.g.,
// auth.GetUserID, auth.GetTenantID) without writing a full struct.
type FuncIdentityResolver struct {
	// ActorFunc extracts the actor ID from context.
	// If it returns "", DefaultActor is used when configured; otherwise Actor fails closed.
	ActorFunc func(ctx context.Context) string

	// TenantFunc extracts the tenant ID from context.
	// It must return a non-empty tenant ID; otherwise TenantID fails closed.
	TenantFunc func(ctx context.Context) string

	// DefaultActor is an explicit fallback actor ID used when ActorFunc is nil or returns "".
	// If empty, Actor fails closed.
	DefaultActor string
}

// maxActorIDLength bounds the length of an actor ID to prevent abuse.
const maxActorIDLength = 256

// maxTenantIDLength bounds the length of a tenant ID to prevent abuse.
const maxTenantIDLength = 256

// Compile-time interface check.
var _ IdentityResolver = (*FuncIdentityResolver)(nil)

// Actor resolves the actor identity from ctx. It fails closed with
// domain.ErrPermissionDenied on nil receiver, nil context, empty/whitespace-only
// IDs, or IDs exceeding maxActorIDLength. When ActorFunc is nil or returns an
// empty string, DefaultActor is used as a fallback.
func (r *FuncIdentityResolver) Actor(ctx context.Context) (domain.Actor, error) {
	if domain.IsNilValue(r) {
		return domain.Actor{}, domain.ErrPermissionDenied
	}

	if ctx == nil {
		return domain.Actor{}, domain.ErrPermissionDenied
	}

	fallback := strings.TrimSpace(r.DefaultActor)

	id := ""
	if r.ActorFunc != nil {
		id = strings.TrimSpace(r.ActorFunc(ctx))
	}

	if id == "" {
		id = fallback
		if id == "" {
			return domain.Actor{}, domain.ErrPermissionDenied
		}
	}

	if len(id) > maxActorIDLength {
		return domain.Actor{}, domain.ErrPermissionDenied
	}

	return domain.Actor{ID: id}, nil
}

// TenantID resolves the tenant identity from ctx. It fails closed with
// domain.ErrPermissionDenied on nil receiver, nil context, nil TenantFunc,
// empty/whitespace-only IDs, or IDs exceeding maxTenantIDLength.
func (r *FuncIdentityResolver) TenantID(ctx context.Context) (string, error) {
	if domain.IsNilValue(r) || r.TenantFunc == nil {
		return "", domain.ErrPermissionDenied
	}

	if ctx == nil {
		return "", domain.ErrPermissionDenied
	}

	tenantID := strings.TrimSpace(r.TenantFunc(ctx))
	if tenantID == "" || len(tenantID) > maxTenantIDLength {
		return "", domain.ErrPermissionDenied
	}

	return tenantID, nil
}
