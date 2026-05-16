//go:build unit

package idempotency

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRedactKey covers the redactKey function.
func TestRedactKey_ProducesHash(t *testing.T) {
	t.Parallel()

	result := redactKey("my-idempotency-key-123")
	require.NotEmpty(t, result)
	// Should produce a hex string (16 chars for 8 bytes)
	assert.Len(t, result, 16)
}

func TestRedactKey_ConsistentHash(t *testing.T) {
	t.Parallel()

	key := "test-key"
	result1 := redactKey(key)
	result2 := redactKey(key)
	assert.Equal(t, result1, result2)
}

func TestRedactKey_DifferentKeys_DifferentHashes(t *testing.T) {
	t.Parallel()

	result1 := redactKey("key-1")
	result2 := redactKey("key-2")
	assert.NotEqual(t, result1, result2)
}

func TestRedactKey_EmptyKey(t *testing.T) {
	t.Parallel()

	result := redactKey("")
	require.NotEmpty(t, result)
	assert.Len(t, result, 16)
}
