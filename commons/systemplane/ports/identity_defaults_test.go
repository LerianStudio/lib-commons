//go:build unit

// Copyright 2025 Lerian Studio.

package ports

import (
	"context"
	"strings"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFuncIdentityResolver_HappyPath(t *testing.T) {
	t.Parallel()

	resolver := &FuncIdentityResolver{
		ActorFunc: func(_ context.Context) string {
			return "user-42"
		},
		TenantFunc: func(_ context.Context) string {
			return "tenant-abc"
		},
	}

	actor, err := resolver.Actor(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "user-42", actor.ID)

	tenant, err := resolver.TenantID(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "tenant-abc", tenant)
}

func TestFuncIdentityResolver_UsesExplicitDefaultActor(t *testing.T) {
	t.Parallel()

	resolver := &FuncIdentityResolver{
		ActorFunc:    nil,
		DefaultActor: "system-bot",
	}

	actor, err := resolver.Actor(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "system-bot", actor.ID)
}

func TestFuncIdentityResolver_ActorFuncEmpty_UsesExplicitDefaultActor(t *testing.T) {
	t.Parallel()

	resolver := &FuncIdentityResolver{
		ActorFunc: func(_ context.Context) string {
			return "   "
		},
		DefaultActor: "service-account",
	}

	actor, err := resolver.Actor(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "service-account", actor.ID)
}

func TestFuncIdentityResolver_ActorID_ExceedsMaxLength_FailsClosed(t *testing.T) {
	t.Parallel()

	longID := strings.Repeat("a", maxActorIDLength+1)

	resolver := &FuncIdentityResolver{
		ActorFunc: func(_ context.Context) string {
			return longID
		},
	}

	_, err := resolver.Actor(context.Background())
	require.ErrorIs(t, err, domain.ErrPermissionDenied)
}

func TestFuncIdentityResolver_ActorID_ExactMaxLength_Allowed(t *testing.T) {
	t.Parallel()

	exactID := strings.Repeat("a", maxActorIDLength)

	resolver := &FuncIdentityResolver{
		ActorFunc: func(_ context.Context) string {
			return exactID
		},
	}

	actor, err := resolver.Actor(context.Background())
	require.NoError(t, err)
	assert.Equal(t, exactID, actor.ID)
}

func TestFuncIdentityResolver_FailsClosedWithoutIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resolver *FuncIdentityResolver
		ctx      context.Context
	}{
		{
			name:     "typed nil receiver",
			resolver: nil,
			ctx:      context.Background(),
		},
		{
			name: "nil context actor",
			resolver: &FuncIdentityResolver{
				ActorFunc: func(_ context.Context) string { return "user-42" },
			},
			ctx: nil,
		},
		{
			name: "actor missing without fallback",
			resolver: &FuncIdentityResolver{
				ActorFunc: nil,
			},
			ctx: context.Background(),
		},
		{
			name: "actor empty without fallback",
			resolver: &FuncIdentityResolver{
				ActorFunc: func(_ context.Context) string { return "" },
			},
			ctx: context.Background(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := tt.resolver.Actor(tt.ctx)
			require.ErrorIs(t, err, domain.ErrPermissionDenied)
		})
	}
}

func TestFuncIdentityResolver_TenantID_FailsClosedWithoutTenantIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resolver *FuncIdentityResolver
		ctx      context.Context
	}{
		{
			name:     "typed nil receiver",
			resolver: nil,
			ctx:      context.Background(),
		},
		{
			name: "nil tenant func",
			resolver: &FuncIdentityResolver{
				TenantFunc: nil,
			},
			ctx: context.Background(),
		},
		{
			name: "nil context",
			resolver: &FuncIdentityResolver{
				TenantFunc: func(_ context.Context) string { return "tenant-1" },
			},
			ctx: nil,
		},
		{
			name: "empty tenant",
			resolver: &FuncIdentityResolver{
				TenantFunc: func(_ context.Context) string { return "" },
			},
			ctx: context.Background(),
		},
		{
			name: "whitespace tenant",
			resolver: &FuncIdentityResolver{
				TenantFunc: func(_ context.Context) string { return "   " },
			},
			ctx: context.Background(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := tt.resolver.TenantID(tt.ctx)
			require.ErrorIs(t, err, domain.ErrPermissionDenied)
		})
	}
}
