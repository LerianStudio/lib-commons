//go:build unit

package core

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
)

func TestTenantSuspendedError(t *testing.T) {
	t.Run("Error returns message when set", func(t *testing.T) {
		err := &TenantSuspendedError{
			TenantID: "tenant-123",
			Status:   "suspended",
			Message:  "service ledger is suspended for this tenant",
		}

		assert.Equal(t, "service ledger is suspended for this tenant", err.Error())
	})

	t.Run("Error returns default message when message is empty", func(t *testing.T) {
		err := &TenantSuspendedError{
			TenantID: "tenant-123",
			Status:   "purged",
		}

		assert.Equal(t, "tenant service is purged for tenant tenant-123", err.Error())
	})

	t.Run("implements error interface", func(t *testing.T) {
		var err error = &TenantSuspendedError{
			TenantID: "tenant-123",
			Status:   "suspended",
			Message:  "test",
		}

		assert.Error(t, err)
	})
}

func TestTenantSuspendedError_NilReceiver(t *testing.T) {
	var err *TenantSuspendedError

	assert.Equal(t, "tenant service is unavailable", err.Error())
}

func TestErrTenantServiceAccessDenied(t *testing.T) {
	assert.Error(t, ErrTenantServiceAccessDenied)
	assert.Equal(t, "tenant service access denied", ErrTenantServiceAccessDenied.Error())

	// Verify errors.Is works with wrapped errors
	wrapped := fmt.Errorf("wrap: %w", ErrTenantServiceAccessDenied)
	assert.ErrorIs(t, wrapped, ErrTenantServiceAccessDenied)
}

func TestIsTenantSuspendedError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error returns false",
			err:      nil,
			expected: false,
		},
		{
			name:     "TenantSuspendedError returns true",
			err:      &TenantSuspendedError{TenantID: "t1", Status: "suspended"},
			expected: true,
		},
		{
			name:     "wrapped TenantSuspendedError returns true",
			err:      fmt.Errorf("outer: %w", &TenantSuspendedError{TenantID: "t1", Status: "suspended"}),
			expected: true,
		},
		{
			name:     "generic error returns false",
			err:      errors.New("some error"),
			expected: false,
		},
		{
			name:     "ErrTenantNotFound returns false",
			err:      ErrTenantNotFound,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsTenantSuspendedError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsTenantNotProvisionedError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error returns false",
			err:      nil,
			expected: false,
		},
		{
			name:     "42P01 error returns true",
			err:      errors.New("ERROR: relation \"table\" does not exist (SQLSTATE 42P01)"),
			expected: true,
		},
		{
			name:     "relation does not exist returns true",
			err:      errors.New("pq: relation \"account\" does not exist"),
			expected: true,
		},
		{
			name:     "generic error returns false",
			err:      errors.New("connection refused"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsTenantNotProvisionedError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestIsTenantNotProvisionedError_TypedDriverErrors locks the typed SQLSTATE path
// (postgres.IsUndefinedTable) for real pgx (*pgconn.PgError) and lib/pq (*pq.Error)
// driver errors, direct and wrapped. The pre-existing tests above only exercise the
// string fallbacks via errors.New text; this pins the driver-type path so it keeps
// detecting undefined_table even if the string fallbacks are ever removed, and
// confirms a typed error carrying a different SQLSTATE is not misclassified.
func TestIsTenantNotProvisionedError_TypedDriverErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "pgx undefined_table returns true",
			err:      &pgconn.PgError{Code: "42P01", Message: "relation does not exist"},
			expected: true,
		},
		{
			name:     "wrapped pgx undefined_table returns true",
			err:      fmt.Errorf("query failed: %w", &pgconn.PgError{Code: "42P01", Message: "relation does not exist"}),
			expected: true,
		},
		{
			name:     "lib/pq undefined_table returns true",
			err:      &pq.Error{Code: pq.ErrorCode("42P01"), Message: "relation does not exist"},
			expected: true,
		},
		{
			name:     "wrapped lib/pq undefined_table returns true",
			err:      fmt.Errorf("query failed: %w", &pq.Error{Code: pq.ErrorCode("42P01"), Message: "relation does not exist"}),
			expected: true,
		},
		{
			name:     "pgx unique_violation returns false",
			err:      &pgconn.PgError{Code: "23505", Message: "duplicate key value"},
			expected: false,
		},
		{
			name:     "lib/pq unique_violation returns false",
			err:      &pq.Error{Code: pq.ErrorCode("23505"), Message: "duplicate key value"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsTenantNotProvisionedError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
