//go:build unit

package core

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsNilInterface covers the IsNilInterface function.
func TestIsNilInterface(t *testing.T) {
	t.Parallel()

	t.Run("nil untyped returns true", func(t *testing.T) {
		assert.True(t, IsNilInterface(nil))
	})

	t.Run("non-nil value returns false", func(t *testing.T) {
		assert.False(t, IsNilInterface("hello"))
	})

	t.Run("typed nil pointer returns true", func(t *testing.T) {
		var p *int
		assert.True(t, IsNilInterface(p))
	})

	t.Run("non-nil pointer returns false", func(t *testing.T) {
		v := 42
		assert.False(t, IsNilInterface(&v))
	})

	t.Run("nil map returns true", func(t *testing.T) {
		var m map[string]string
		assert.True(t, IsNilInterface(m))
	})

	t.Run("non-nil map returns false", func(t *testing.T) {
		m := map[string]string{"a": "b"}
		assert.False(t, IsNilInterface(m))
	})

	t.Run("nil slice returns true", func(t *testing.T) {
		var s []string
		assert.True(t, IsNilInterface(s))
	})

	t.Run("non-nil slice returns false", func(t *testing.T) {
		s := []string{"x"}
		assert.False(t, IsNilInterface(s))
	})

	t.Run("nil func returns true", func(t *testing.T) {
		var fn func()
		assert.True(t, IsNilInterface(fn))
	})

	t.Run("non-nil func returns false", func(t *testing.T) {
		assert.False(t, IsNilInterface(func() {}))
	})

	t.Run("nil chan returns true", func(t *testing.T) {
		var ch chan struct{}
		assert.True(t, IsNilInterface(ch))
	})

	t.Run("non-nil chan returns false", func(t *testing.T) {
		ch := make(chan struct{})
		assert.False(t, IsNilInterface(ch))
	})

	t.Run("integer (non-nilable kind) returns false", func(t *testing.T) {
		assert.False(t, IsNilInterface(42))
	})

	t.Run("bool (non-nilable kind) returns false", func(t *testing.T) {
		assert.False(t, IsNilInterface(false))
	})
}

// TestIsCircuitBreakerOpenError covers the IsCircuitBreakerOpenError function.
func TestIsCircuitBreakerOpenError(t *testing.T) {
	t.Parallel()

	t.Run("returns true for ErrCircuitBreakerOpen", func(t *testing.T) {
		assert.True(t, IsCircuitBreakerOpenError(ErrCircuitBreakerOpen))
	})

	t.Run("returns true for wrapped ErrCircuitBreakerOpen", func(t *testing.T) {
		wrapped := errors.Join(errors.New("outer"), ErrCircuitBreakerOpen)
		assert.True(t, IsCircuitBreakerOpenError(wrapped))
	})

	t.Run("returns false for nil", func(t *testing.T) {
		assert.False(t, IsCircuitBreakerOpenError(nil))
	})

	t.Run("returns false for unrelated error", func(t *testing.T) {
		assert.False(t, IsCircuitBreakerOpenError(errors.New("something else")))
	})

	t.Run("returns false for ErrTenantNotFound", func(t *testing.T) {
		assert.False(t, IsCircuitBreakerOpenError(ErrTenantNotFound))
	})
}

// TestIsTenantPurgedError covers the IsTenantPurgedError function.
func TestIsTenantPurgedError(t *testing.T) {
	t.Parallel()

	t.Run("returns true for purged status", func(t *testing.T) {
		err := &TenantSuspendedError{TenantID: "t1", Status: "purged"}
		assert.True(t, IsTenantPurgedError(err))
	})

	t.Run("returns false for suspended (not purged) status", func(t *testing.T) {
		err := &TenantSuspendedError{TenantID: "t1", Status: "suspended"}
		assert.False(t, IsTenantPurgedError(err))
	})

	t.Run("returns false for nil error", func(t *testing.T) {
		assert.False(t, IsTenantPurgedError(nil))
	})

	t.Run("returns false for generic error", func(t *testing.T) {
		assert.False(t, IsTenantPurgedError(errors.New("some error")))
	})

	t.Run("returns true for wrapped purged error", func(t *testing.T) {
		wrapped := errors.Join(errors.New("wrap"), &TenantSuspendedError{Status: "purged"})
		assert.True(t, IsTenantPurgedError(wrapped))
	})
}

// TestIsValidTenantID covers the IsValidTenantID function.
func TestIsValidTenantID(t *testing.T) {
	t.Parallel()

	validCases := []struct {
		name string
		id   string
	}{
		{"simple alphanumeric", "tenant123"},
		{"uppercase", "TENANT"},
		{"mixed case", "Tenant-ABC_def"},
		{"single char", "a"},
		{"with hyphen", "tenant-abc"},
		{"with underscore", "tenant_abc"},
		{"starts with digit", "1tenant"},
		{"all digits", "123"},
	}

	for _, tc := range validCases {
		t.Run("valid: "+tc.name, func(t *testing.T) {
			t.Parallel()
			assert.True(t, IsValidTenantID(tc.id), "expected valid tenant ID: %q", tc.id)
		})
	}

	invalidCases := []struct {
		name string
		id   string
	}{
		{"empty string", ""},
		{"leading hyphen", "-tenant"},
		{"leading underscore", "_tenant"},
		{"contains space", "tenant abc"},
		{"contains dot", "tenant.abc"},
		{"contains slash", "tenant/abc"},
		{"contains at sign", "tenant@abc"},
	}

	for _, tc := range invalidCases {
		t.Run("invalid: "+tc.name, func(t *testing.T) {
			t.Parallel()
			assert.False(t, IsValidTenantID(tc.id), "expected invalid tenant ID: %q", tc.id)
		})
	}

	t.Run("invalid: exceeds max length", func(t *testing.T) {
		longID := make([]byte, MaxTenantIDLength+1)
		for i := range longID {
			longID[i] = 'a'
		}

		assert.False(t, IsValidTenantID(string(longID)))
	})

	t.Run("valid: exactly max length", func(t *testing.T) {
		maxID := make([]byte, MaxTenantIDLength)
		for i := range maxID {
			maxID[i] = 'a'
		}

		assert.True(t, IsValidTenantID(string(maxID)))
	})
}
