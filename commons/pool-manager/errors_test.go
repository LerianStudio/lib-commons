package poolmanager

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTenantErrors_Defined(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "ErrTenantNotFound should be defined",
			err:      ErrTenantNotFound,
			expected: "tenant not found",
		},
		{
			name:     "ErrTenantAlreadyExists should be defined",
			err:      ErrTenantAlreadyExists,
			expected: "tenant already exists",
		},
		{
			name:     "ErrInvalidTenantID should be defined",
			err:      ErrInvalidTenantID,
			expected: "invalid tenant ID",
		},
		{
			name:     "ErrTenantContextMissing should be defined",
			err:      ErrTenantContextMissing,
			expected: "tenant context missing",
		},
		{
			name:     "ErrTenantInactive should be defined",
			err:      ErrTenantInactive,
			expected: "tenant is inactive",
		},
		{
			name:     "ErrTenantSuspended should be defined",
			err:      ErrTenantSuspended,
			expected: "tenant is suspended",
		},
		{
			name:     "ErrTenantDeleted should be defined",
			err:      ErrTenantDeleted,
			expected: "tenant is deleted",
		},
		{
			name:     "ErrCrossTenantAccess should be defined",
			err:      ErrCrossTenantAccess,
			expected: "cross-tenant access denied",
		},
		{
			name:     "ErrTenantIsolationViolation should be defined",
			err:      ErrTenantIsolationViolation,
			expected: "tenant isolation violation",
		},
		{
			name:     "ErrTenantConnectionRequired should be defined",
			err:      ErrTenantConnectionRequired,
			expected: "tenant connection required but not found in context",
		},
		{
			name:     "ErrNoConnectionAvailable should be defined",
			err:      ErrNoConnectionAvailable,
			expected: "no database connection available",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotNil(t, tt.err, "error should not be nil")
			assert.Equal(t, tt.expected, tt.err.Error(), "error message should match")
		})
	}
}

func TestTenantErrors_ErrorWrapping(t *testing.T) {
	tests := []struct {
		name        string
		baseErr     error
		wrapMessage string
	}{
		{
			name:        "ErrTenantNotFound can be wrapped",
			baseErr:     ErrTenantNotFound,
			wrapMessage: "failed to get tenant: %w",
		},
		{
			name:        "ErrInvalidTenantID can be wrapped",
			baseErr:     ErrInvalidTenantID,
			wrapMessage: "validation failed: %w",
		},
		{
			name:        "ErrTenantContextMissing can be wrapped",
			baseErr:     ErrTenantContextMissing,
			wrapMessage: "middleware error: %w",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify the base error exists
			require.NotNil(t, tt.baseErr)

			// Wrap the error using fmt.Errorf with %w verb
			wrappedErr := fmt.Errorf(tt.wrapMessage, tt.baseErr)

			// Verify we can unwrap and identify the original error
			assert.True(t, errors.Is(wrappedErr, tt.baseErr), "wrapped error should match base error via errors.Is")

			// Verify errors.Unwrap returns the base error
			unwrappedErr := errors.Unwrap(wrappedErr)
			assert.Equal(t, tt.baseErr, unwrappedErr, "errors.Unwrap should return the base error")

			// Verify the wrapped error message contains context
			assert.Contains(t, wrappedErr.Error(), tt.baseErr.Error(), "wrapped error should contain base error message")
		})
	}
}

func TestTenantErrors_Is(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		target  error
		matches bool
	}{
		{
			name:    "ErrTenantNotFound matches itself",
			err:     ErrTenantNotFound,
			target:  ErrTenantNotFound,
			matches: true,
		},
		{
			name:    "ErrTenantAlreadyExists matches itself",
			err:     ErrTenantAlreadyExists,
			target:  ErrTenantAlreadyExists,
			matches: true,
		},
		{
			name:    "ErrTenantNotFound does not match ErrInvalidTenantID",
			err:     ErrTenantNotFound,
			target:  ErrInvalidTenantID,
			matches: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotNil(t, tt.err)
			require.NotNil(t, tt.target)

			result := errors.Is(tt.err, tt.target)
			assert.Equal(t, tt.matches, result, "errors.Is result should match expected")
		})
	}
}

func TestTenantErrors_AreDistinct(t *testing.T) {
	// All errors should be distinct from each other
	allErrors := []error{
		ErrTenantNotFound,
		ErrTenantAlreadyExists,
		ErrInvalidTenantID,
		ErrTenantContextMissing,
		ErrTenantInactive,
		ErrTenantSuspended,
		ErrTenantDeleted,
		ErrCrossTenantAccess,
		ErrTenantIsolationViolation,
		ErrTenantConnectionRequired,
		ErrNoConnectionAvailable,
	}

	for i, err1 := range allErrors {
		require.NotNil(t, err1, "error at index %d should not be nil", i)

		for j, err2 := range allErrors {
			if i == j {
				continue
			}

			require.NotNil(t, err2, "error at index %d should not be nil", j)
			assert.NotEqual(t, err1.Error(), err2.Error(),
				"errors at index %d and %d should have different messages", i, j)
		}
	}
}
