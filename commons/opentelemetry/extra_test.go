//go:build unit

package opentelemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewAlwaysMaskRedactor covers the constructor.
func TestNewAlwaysMaskRedactor(t *testing.T) {
	t.Parallel()

	r := NewAlwaysMaskRedactor()
	require.NotNil(t, r)
	assert.NotEmpty(t, r.rules)
}

// TestNewAlwaysMaskRedactor_HasRules covers that rules are not empty.
func TestNewAlwaysMaskRedactor_HasRules(t *testing.T) {
	t.Parallel()

	r := NewAlwaysMaskRedactor()
	require.NotNil(t, r)
	assert.Len(t, r.rules, 1)
	assert.Equal(t, RedactionMask, r.rules[0].Action)
}

// TestSetSpanAttributeForParam_NilCtx covers the nil fiber.Ctx guard.
func TestSetSpanAttributeForParam_NilCtx(t *testing.T) {
	t.Parallel()

	// Should not panic with nil Ctx
	assert.NotPanics(t, func() {
		SetSpanAttributeForParam(nil, "id", "123", "account")
	})
}

// TestExtractHTTPContext_NilCtx covers the nil fiber.Ctx guard.
func TestExtractHTTPContext_NilCtx(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	result := ExtractHTTPContext(ctx, nil)
	// Should return the original context when fiber.Ctx is nil
	assert.NotNil(t, result)
}

// TestShutdownAll_EmptyComponents covers the empty slice path.
func TestShutdownAll_EmptyComponents(t *testing.T) {
	t.Parallel()

	// Should not panic with empty components
	assert.NotPanics(t, func() {
		shutdownAll(context.Background(), nil)
	})

	assert.NotPanics(t, func() {
		shutdownAll(context.Background(), []shutdownable{})
	})
}

// TestAttrBagSpanProcessor_OnEnd covers the no-op OnEnd.
func TestAttrBagSpanProcessor_OnEnd(t *testing.T) {
	t.Parallel()

	p := AttrBagSpanProcessor{}
	assert.NotPanics(t, func() {
		p.OnEnd(nil)
	})
}

// TestRedactingAttrBagSpanProcessor_OnEnd covers the no-op OnEnd.
func TestRedactingAttrBagSpanProcessor_OnEnd(t *testing.T) {
	t.Parallel()

	p := RedactingAttrBagSpanProcessor{}
	assert.NotPanics(t, func() {
		p.OnEnd(nil)
	})
}
