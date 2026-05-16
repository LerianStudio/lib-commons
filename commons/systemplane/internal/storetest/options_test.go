//go:build unit

package storetest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestSkipSubtest covers the SkipSubtest option.
func TestSkipSubtest_SetsSkipEntry(t *testing.T) {
	t.Parallel()

	cfg := &config{}
	opt := SkipSubtest("TenantSomeTest")
	opt(cfg)

	assert.NotNil(t, cfg.skip)
	_, ok := cfg.skip["TenantSomeTest"]
	assert.True(t, ok)
}

// TestWithEventSettle covers the WithEventSettle option.
func TestWithEventSettle_SetsSettle(t *testing.T) {
	t.Parallel()

	cfg := &config{}
	opt := WithEventSettle(200 * time.Millisecond)
	opt(cfg)

	assert.Equal(t, 200*time.Millisecond, cfg.settle)
}

func TestWithEventSettle_NegativeIgnored(t *testing.T) {
	t.Parallel()

	cfg := &config{settle: 100 * time.Millisecond}
	opt := WithEventSettle(-1)
	opt(cfg)

	// Negative values should NOT update the field
	assert.Equal(t, 100*time.Millisecond, cfg.settle)
}

// TestSkipSubtest_WithAlias covers alias expansion.
func TestSkipSubtest_WithAlias(t *testing.T) {
	t.Parallel()

	cfg := &config{}
	// "TenantOnChangeReceivesDelete" has an alias
	SkipSubtest("TenantOnChangeReceivesDelete")(cfg)

	_, hasMain := cfg.skip["TenantOnChangeReceivesDelete"]
	_, hasAlias := cfg.skip["TenantSubscribeReceivesDeleteEvent"]
	assert.True(t, hasMain)
	assert.True(t, hasAlias)
}
