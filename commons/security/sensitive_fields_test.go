//go:build unit

package security

import (
	"testing"

	"github.com/LerianStudio/lib-observability/v4/redaction"
	"github.com/stretchr/testify/assert"
)

// The taxonomy is lib-observability's and tested there; this proves only the delegation.
func TestSensitiveFieldsDelegateToRedaction(t *testing.T) {
	t.Parallel()

	assert.Equal(t, redaction.DefaultSensitiveFields(), DefaultSensitiveFields())
	assert.Equal(t, redaction.DefaultSensitiveFieldsMap(), DefaultSensitiveFieldsMap())
	assert.True(t, IsSensitiveField("password"))
	assert.False(t, IsSensitiveField("status"))
}
