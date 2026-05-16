//go:build unit

package outboxtest

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	"github.com/stretchr/testify/assert"
)

// TestSkipSubtest covers the SkipSubtest option.
func TestSkipSubtest_SetsSkipEntry(t *testing.T) {
	t.Parallel()

	cfg := &runConfig{}
	opt := SkipSubtest("TestSomeSubtest")
	opt(cfg)

	assert.True(t, cfg.shouldSkip("TestSomeSubtest"))
	assert.False(t, cfg.shouldSkip("TestOtherSubtest"))
}

func TestSkipSubtest_MultipleSkips(t *testing.T) {
	t.Parallel()

	cfg := &runConfig{}
	SkipSubtest("A")(cfg)
	SkipSubtest("B")(cfg)

	assert.True(t, cfg.shouldSkip("A"))
	assert.True(t, cfg.shouldSkip("B"))
	assert.False(t, cfg.shouldSkip("C"))
}

// TestWithTransactionFactory covers the WithTransactionFactory option.
func TestWithTransactionFactory_SetsFactory(t *testing.T) {
	t.Parallel()

	cfg := &runConfig{}
	factory := func(_ *testing.T, _ context.Context) (outbox.Tx, func()) {
		return nil, func() {}
	}

	opt := WithTransactionFactory(factory)
	opt(cfg)

	assert.NotNil(t, cfg.txFactory)
}

// TestShouldSkip_NilConfig covers the nil config path.
func TestShouldSkip_NilConfig(t *testing.T) {
	t.Parallel()

	var cfg *runConfig
	assert.False(t, cfg.shouldSkip("anything"))
}

// TestShouldSkip_NilSkipMap covers the nil map path.
func TestShouldSkip_NilSkipMap(t *testing.T) {
	t.Parallel()

	cfg := &runConfig{}
	assert.False(t, cfg.shouldSkip("anything"))
}
