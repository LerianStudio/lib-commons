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
	// ActorFunc extracts the actor ID from context. If nil or returns "", DefaultActor is used.
	ActorFunc func(ctx context.Context) string

	// TenantFunc extracts the tenant ID from context. If nil or returns "", empty string is returned (not an error).
	TenantFunc func(ctx context.Context) string

	// DefaultActor is the fallback actor ID when ActorFunc is nil or returns "".
	// Defaults to "anonymous" if empty.
	DefaultActor string
}

// Compile-time interface check.
var _ IdentityResolver = (*FuncIdentityResolver)(nil)

func (r *FuncIdentityResolver) Actor(ctx context.Context) (domain.Actor, error) {
	if domain.IsNilValue(r) {
		return domain.Actor{ID: "anonymous"}, nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	id := ""
	if r.ActorFunc != nil {
		id = strings.TrimSpace(r.ActorFunc(ctx))
	}

	if id == "" {
		id = r.DefaultActor
		if id == "" {
			id = "anonymous"
		}
	}

	return domain.Actor{ID: id}, nil
}

func (r *FuncIdentityResolver) TenantID(ctx context.Context) (string, error) {
	if domain.IsNilValue(r) || r.TenantFunc == nil {
		return "", nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	return strings.TrimSpace(r.TenantFunc(ctx)), nil
}
