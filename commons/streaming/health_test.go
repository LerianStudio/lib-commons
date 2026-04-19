//go:build unit

package streaming

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestHealthState_String covers every enum transition including the
// out-of-range defensive branch (important because Healthy is the zero
// value and callers may construct a HealthError with an unset state).
func TestHealthState_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state HealthState
		want  string
	}{
		{"Healthy", Healthy, "healthy"},
		{"Degraded", Degraded, "degraded"},
		{"Down", Down, "down"},
		{"out-of-range", HealthState(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.state.String(); got != tt.want {
				t.Errorf("HealthState(%d).String() = %q; want %q", tt.state, got, tt.want)
			}
		})
	}
}

// TestHealthError_Error_WithCause verifies the error message format
// and that the cause flows through sanitizeBrokerURL (no SASL leak).
func TestHealthError_Error_WithCause(t *testing.T) {
	t.Parallel()

	cause := fmt.Errorf("dial sasl://user:hunter2@broker:9092: i/o timeout")
	he := NewHealthError(Degraded, cause)

	got := he.Error()

	if !strings.Contains(got, "degraded") {
		t.Errorf("Error() = %q; want to contain 'degraded'", got)
	}

	if strings.Contains(got, "hunter2") {
		t.Errorf("Error() = %q; leaks credential 'hunter2'", got)
	}

	// The full credential pair "user:hunter2" must not appear verbatim.
	// The sanitizer preserves the username but replaces the password, so
	// "user:****" is expected — but the original pair must be gone.
	if strings.Contains(got, "user:hunter2") {
		t.Errorf("Error() = %q; leaks full credential pair 'user:hunter2'", got)
	}
}

// TestHealthError_Error_NoCause covers the branch where cause is nil.
func TestHealthError_Error_NoCause(t *testing.T) {
	t.Parallel()

	he := NewHealthError(Down, nil)
	got := he.Error()

	if !strings.Contains(got, "down") {
		t.Errorf("Error() = %q; want to contain 'down'", got)
	}
}

// TestHealthError_NilReceiver: Error/State/Unwrap must not panic on a
// nil *HealthError. Degrades to the zero values so call sites that
// indiscriminately dereference an error don't fail safe checks.
func TestHealthError_NilReceiver(t *testing.T) {
	t.Parallel()

	var he *HealthError

	if got := he.Error(); got != "<nil>" {
		t.Errorf("nil.Error() = %q; want %q", got, "<nil>")
	}

	if got := he.State(); got != Healthy {
		t.Errorf("nil.State() = %q; want %q", got, Healthy)
	}

	if got := he.Unwrap(); got != nil {
		t.Errorf("nil.Unwrap() = %v; want nil", got)
	}
}

// TestHealthError_Unwrap integrates with errors.Is walking so callers can
// match the root cause (e.g., context.Canceled) without reaching into
// HealthError fields.
func TestHealthError_Unwrap(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("root")
	he := NewHealthError(Down, sentinel)

	if !errors.Is(he, sentinel) {
		t.Errorf("errors.Is(he, sentinel) = false; want true")
	}

	if got := he.Unwrap(); got != sentinel {
		t.Errorf("Unwrap() = %v; want %v", got, sentinel)
	}
}

// TestHealthError_ErrorsAs exercises errors.As extraction through a
// wrapping chain. Common pattern: Healthy returns err; caller walks the
// chain to find *HealthError and checks State().
func TestHealthError_ErrorsAs(t *testing.T) {
	t.Parallel()

	he := NewHealthError(Degraded, errors.New("ping failed"))
	wrapped := fmt.Errorf("service probe failed: %w", he)

	var target *HealthError
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As did not extract *HealthError")
	}

	if target.State() != Degraded {
		t.Errorf("extracted state = %q; want %q", target.State(), Degraded)
	}
}

// TestHealthError_Unwrap_DirectSentinel pins the Unwrap contract against a
// known sentinel. Explicitly verifies that errors.Is walks straight to the
// wrapped ErrNilProducer so callers do not need to reach into HealthError
// internals. This complements TestHealthError_Unwrap (which uses a generic
// root error) by exercising the exact shape used by (*Producer).Healthy on
// a nil receiver.
func TestHealthError_Unwrap_DirectSentinel(t *testing.T) {
	t.Parallel()

	he := NewHealthError(Down, ErrNilProducer)

	if !errors.Is(he, ErrNilProducer) {
		t.Errorf("errors.Is(he, ErrNilProducer) = false; want true")
	}

	if got := he.Unwrap(); got != ErrNilProducer {
		t.Errorf("Unwrap() = %v; want ErrNilProducer", got)
	}

	if he.State() != Down {
		t.Errorf("State() = %q; want Down", he.State())
	}
}
