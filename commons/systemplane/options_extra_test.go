//go:build unit

package systemplane

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestWithTenantSchemaEnabled covers the option.
func TestWithTenantSchemaEnabled(t *testing.T) {
	t.Parallel()

	cfg := &clientConfig{}
	opt := WithTenantSchemaEnabled()
	opt(cfg)
	assert.True(t, cfg.tenantSchemaEnabled)
}

// TestWithTelemetry_Nil covers the nil guard in WithTelemetry.
func TestWithTelemetry_Nil(t *testing.T) {
	t.Parallel()

	cfg := &clientConfig{}
	opt := WithTelemetry(nil)
	opt(cfg)
	assert.Nil(t, cfg.telemetry)
}

// TestWithLogger_Nil covers the nil guard in WithLogger.
func TestWithLogger_Nil(t *testing.T) {
	t.Parallel()

	cfg := &clientConfig{}
	opt := WithLogger(nil)
	opt(cfg)
	assert.Nil(t, cfg.logger)
}
