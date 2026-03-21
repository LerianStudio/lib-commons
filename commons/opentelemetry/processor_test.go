//go:build unit

package opentelemetry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
)

func TestRedactAttributesByKey(t *testing.T) {
	t.Parallel()

	redactor, err := NewRedactor([]RedactionRule{
		{FieldPattern: `(?i)^password$`, Action: RedactionMask},
		{FieldPattern: `(?i)^token$`, Action: RedactionDrop},
		{FieldPattern: `(?i)^document$`, Action: RedactionHash},
	}, "***")
	require.NoError(t, err)

	attrs := []attribute.KeyValue{
		attribute.String("user.id", "u1"),
		attribute.String("user.password", "secret"),
		attribute.String("auth.token", "tok_123"),
		attribute.String("customer.document", "123456789"),
	}

	redacted := redactAttributesByKey(attrs, redactor)

	values := make(map[string]string, len(redacted))
	for _, attr := range redacted {
		values[string(attr.Key)] = attr.Value.AsString()
	}

	assert.Equal(t, "u1", values["user.id"])
	assert.Equal(t, "***", values["user.password"])
	assert.NotContains(t, values, "auth.token")
	assert.Contains(t, values["customer.document"], "sha256:")
	assert.NotEqual(t, "123456789", values["customer.document"])
}
