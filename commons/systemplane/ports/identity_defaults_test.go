//go:build unit

// Copyright 2025 Lerian Studio.

package ports

import (
	"context"
	"testing"

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

func TestFuncIdentityResolver_NilActorFunc(t *testing.T) {
	t.Parallel()

	resolver := &FuncIdentityResolver{
		ActorFunc: nil,
	}

	actor, err := resolver.Actor(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "anonymous", actor.ID)
}

func TestFuncIdentityResolver_EmptyActorReturn(t *testing.T) {
	t.Parallel()

	resolver := &FuncIdentityResolver{
		ActorFunc: func(_ context.Context) string {
			return ""
		},
	}

	actor, err := resolver.Actor(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "anonymous", actor.ID)
}

func TestFuncIdentityResolver_WhitespaceActorReturn(t *testing.T) {
	t.Parallel()

	resolver := &FuncIdentityResolver{
		ActorFunc: func(_ context.Context) string {
			return "   "
		},
	}

	actor, err := resolver.Actor(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "anonymous", actor.ID)
}

func TestFuncIdentityResolver_CustomDefaultActor(t *testing.T) {
	t.Parallel()

	resolver := &FuncIdentityResolver{
		ActorFunc:    nil,
		DefaultActor: "system-bot",
	}

	actor, err := resolver.Actor(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "system-bot", actor.ID)
}

func TestFuncIdentityResolver_NilTenantFunc(t *testing.T) {
	t.Parallel()

	resolver := &FuncIdentityResolver{
		TenantFunc: nil,
	}

	tenant, err := resolver.TenantID(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "", tenant)
}

func TestFuncIdentityResolver_EmptyTenantReturn(t *testing.T) {
	t.Parallel()

	resolver := &FuncIdentityResolver{
		TenantFunc: func(_ context.Context) string {
			return ""
		},
	}

	tenant, err := resolver.TenantID(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "", tenant)
}

func TestFuncIdentityResolver_WhitespaceTenantReturn(t *testing.T) {
	t.Parallel()

	resolver := &FuncIdentityResolver{
		TenantFunc: func(_ context.Context) string {
			return "   "
		},
	}

	tenant, err := resolver.TenantID(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "", tenant)
}

func TestFuncIdentityResolver_NeverReturnsError(t *testing.T) {
	t.Parallel()

	// Exhaustively verify that no combination of inputs produces an error.
	configs := []struct {
		name     string
		resolver *FuncIdentityResolver
	}{
		{
			name:     "zero value",
			resolver: &FuncIdentityResolver{},
		},
		{
			name: "both funcs set",
			resolver: &FuncIdentityResolver{
				ActorFunc:  func(_ context.Context) string { return "u" },
				TenantFunc: func(_ context.Context) string { return "t" },
			},
		},
		{
			name: "both funcs return empty",
			resolver: &FuncIdentityResolver{
				ActorFunc:  func(_ context.Context) string { return "" },
				TenantFunc: func(_ context.Context) string { return "" },
			},
		},
		{
			name: "custom default with nil funcs",
			resolver: &FuncIdentityResolver{
				DefaultActor: "fallback",
			},
		},
	}

	for _, cfg := range configs {
		t.Run(cfg.name, func(t *testing.T) {
			t.Parallel()

			_, actorErr := cfg.resolver.Actor(context.Background())
			assert.NoError(t, actorErr)

			_, tenantErr := cfg.resolver.TenantID(context.Background())
			assert.NoError(t, tenantErr)
		})
	}
}

func TestFuncIdentityResolver_TypedNilReceiver(t *testing.T) {
	t.Parallel()

	var resolver *FuncIdentityResolver

	actor, err := resolver.Actor(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "anonymous", actor.ID)

	tenant, err := resolver.TenantID(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "", tenant)
}

func TestFuncIdentityResolver_NilContext_UsesBackground(t *testing.T) {
	t.Parallel()

	resolver := &FuncIdentityResolver{
		ActorFunc: func(ctx context.Context) string {
			assert.NotNil(t, ctx)
			return "user-42"
		},
		TenantFunc: func(ctx context.Context) string {
			assert.NotNil(t, ctx)
			return "tenant-abc"
		},
	}

	actor, err := resolver.Actor(nil)
	require.NoError(t, err)
	assert.Equal(t, "user-42", actor.ID)

	tenant, err := resolver.TenantID(nil)
	require.NoError(t, err)
	assert.Equal(t, "tenant-abc", tenant)
}
