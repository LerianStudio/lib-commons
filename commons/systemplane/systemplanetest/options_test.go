//go:build unit

package systemplanetest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestSkipSubtest covers the SkipSubtest option.
func TestSkipSubtest_SetsEntry(t *testing.T) {
	t.Parallel()

	cfg := &runConfig{}
	opt := SkipSubtest("RegisterBeforeStart")
	opt.applyRunOption(cfg)

	assert.NotNil(t, cfg.skip)
	_, ok := cfg.skip["RegisterBeforeStart"]
	assert.True(t, ok)
}

// TestWithEventSettle covers the WithEventSettle option.
func TestWithEventSettle_SetsSettle(t *testing.T) {
	t.Parallel()

	cfg := &runConfig{}
	opt := WithEventSettle(100 * time.Millisecond)
	opt.applyRunOption(cfg)

	assert.Equal(t, 100*time.Millisecond, cfg.eventSettle)
}

func TestWithEventSettle_NegativeIgnored(t *testing.T) {
	t.Parallel()

	cfg := &runConfig{eventSettle: 50 * time.Millisecond}
	opt := WithEventSettle(-1)
	opt.applyRunOption(cfg)

	assert.Equal(t, 50*time.Millisecond, cfg.eventSettle)
}

// TestShouldSkip_NilConfig covers nil config guard.
func TestShouldSkip_NilRunConfig(t *testing.T) {
	t.Parallel()

	var cfg *runConfig
	assert.False(t, cfg.shouldSkip("anything"))
}

// TestSkipSubtest_WithLegacyAlias covers alias expansion.
func TestSkipSubtest_WithLegacyAlias(t *testing.T) {
	t.Parallel()

	cfg := &runConfig{}
	opt := SkipSubtest("TenantSubscribeReceivesSetEvent")
	opt.applyRunOption(cfg)

	_, hasMain := cfg.skip["TenantSubscribeReceivesSetEvent"]
	_, hasAlias := cfg.skip["TenantOnChangeReceivesSet"]
	assert.True(t, hasMain)
	assert.True(t, hasAlias)
}
