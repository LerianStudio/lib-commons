//go:build unit

package systemplane

import (
	"testing"

	liblog "github.com/LerianStudio/lib-observability/log"
	"github.com/stretchr/testify/assert"
)

// TestList_NilClient covers nil receiver guard.
func TestList_NilClient(t *testing.T) {
	t.Parallel()

	var c *Client
	result := c.List("global")
	assert.Nil(t, result)
}

// TestKeyDescription_NilClient covers nil receiver guard.
func TestKeyDescription_NilClient(t *testing.T) {
	t.Parallel()

	var c *Client
	result := c.KeyDescription("global", "log.level")
	assert.Equal(t, "", result)
}

// TestKeyDescription_UnregisteredKey covers the unregistered path.
func TestKeyDescription_UnregisteredKey(t *testing.T) {
	t.Parallel()

	c := &Client{
		registry:            make(map[nskey]keyDef),
		tenantScopedRegistry: make(map[nskey]struct{}),
	}

	result := c.KeyDescription("global", "nonexistent.key")
	assert.Equal(t, "", result)
}

// TestKeyRedaction_NilClient covers nil receiver guard.
func TestKeyRedaction_NilClient(t *testing.T) {
	t.Parallel()

	var c *Client
	result := c.KeyRedaction("global", "log.level")
	assert.Equal(t, RedactNone, result)
}

// TestKeyRedaction_UnregisteredKey covers unregistered key.
func TestKeyRedaction_UnregisteredKey(t *testing.T) {
	t.Parallel()

	c := &Client{
		registry:            make(map[nskey]keyDef),
		tenantScopedRegistry: make(map[nskey]struct{}),
	}

	result := c.KeyRedaction("global", "nonexistent.key")
	assert.Equal(t, RedactNone, result)
}

// TestKeyStatus_NilClient covers nil receiver guard.
func TestKeyStatus_NilClient(t *testing.T) {
	t.Parallel()

	var c *Client
	registered, tenantScoped := c.KeyStatus("global", "log.level")
	assert.False(t, registered)
	assert.False(t, tenantScoped)
}

// TestKeyStatus_UnregisteredKey covers the unregistered path.
func TestKeyStatus_UnregisteredKey(t *testing.T) {
	t.Parallel()

	c := &Client{
		registry:            make(map[nskey]keyDef),
		tenantScopedRegistry: make(map[nskey]struct{}),
	}

	registered, tenantScoped := c.KeyStatus("global", "nonexistent")
	assert.False(t, registered)
	assert.False(t, tenantScoped)
}

// TestLogger_NilClient covers nil receiver guard.
func TestLogger_NilClient(t *testing.T) {
	t.Parallel()

	var c *Client
	result := c.Logger()
	// Should return a nop logger, not nil
	assert.NotNil(t, result)
}

// TestLogger_NilLogger covers nil logger path.
func TestLogger_NilLogger(t *testing.T) {
	t.Parallel()

	c := &Client{
		registry:             make(map[nskey]keyDef),
		tenantScopedRegistry: make(map[nskey]struct{}),
		logger:               nil,
	}
	result := c.Logger()
	assert.NotNil(t, result)
}

// TestLogger_WithLogger covers the non-nil logger path.
func TestLogger_WithLogger(t *testing.T) {
	t.Parallel()

	logger := liblog.NewNop()
	c := &Client{
		registry:            make(map[nskey]keyDef),
		tenantScopedRegistry: make(map[nskey]struct{}),
		logger:              logger,
	}
	result := c.Logger()
	assert.Equal(t, logger, result)
}
