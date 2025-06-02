package commons

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestInternalKey(t *testing.T) {
	orgID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	ledgerID := uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")

	tests := []struct {
		name     string
		orgID    uuid.UUID
		ledgerID uuid.UUID
		key      string
		expected string
	}{
		{
			name:     "Normal key",
			orgID:    orgID,
			ledgerID: ledgerID,
			key:      "test-key",
			expected: "550e8400-e29b-41d4-a716-446655440000:6ba7b810-9dad-11d1-80b4-00c04fd430c8:test-key",
		},
		{
			name:     "Empty key",
			orgID:    orgID,
			ledgerID: ledgerID,
			key:      "",
			expected: "550e8400-e29b-41d4-a716-446655440000:6ba7b810-9dad-11d1-80b4-00c04fd430c8:",
		},
		{
			name:     "Key with special characters",
			orgID:    orgID,
			ledgerID: ledgerID,
			key:      "key:with:colons",
			expected: "550e8400-e29b-41d4-a716-446655440000:6ba7b810-9dad-11d1-80b4-00c04fd430c8:key:with:colons",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := InternalKey(tt.orgID, tt.ledgerID, tt.key)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestLockInternalKey(t *testing.T) {
	orgID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	ledgerID := uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")

	tests := []struct {
		name     string
		orgID    uuid.UUID
		ledgerID uuid.UUID
		key      string
		expected string
	}{
		{
			name:     "Normal key",
			orgID:    orgID,
			ledgerID: ledgerID,
			key:      "test-key",
			expected: "lock:550e8400-e29b-41d4-a716-446655440000:6ba7b810-9dad-11d1-80b4-00c04fd430c8:test-key",
		},
		{
			name:     "Empty key",
			orgID:    orgID,
			ledgerID: ledgerID,
			key:      "",
			expected: "lock:550e8400-e29b-41d4-a716-446655440000:6ba7b810-9dad-11d1-80b4-00c04fd430c8:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := LockInternalKey(tt.orgID, tt.ledgerID, tt.key)
			assert.Equal(t, tt.expected, result)
		})
	}
}
