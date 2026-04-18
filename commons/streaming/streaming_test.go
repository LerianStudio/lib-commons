//go:build unit

package streaming

import (
	"errors"
	"strings"
	"testing"
)

// TestIsCallerError exercises the caller-vs-infrastructure fault truth table
// from TRD §C9 (DX-B03). Caller errors are deterministic faults the caller can
// fix: missing tenant, wrong payload shape, bad config. Infrastructure errors
// are runtime conditions outside the caller's control.
func TestIsCallerError(t *testing.T) {
	t.Parallel()

	callerCases := []struct {
		name string
		err  error
	}{
		{"ClassSerialization", &EmitError{Class: ClassSerialization}},
		{"ClassValidation", &EmitError{Class: ClassValidation}},
		{"ClassAuth", &EmitError{Class: ClassAuth}},
		{"ErrMissingTenantID", ErrMissingTenantID},
		{"ErrMissingSource", ErrMissingSource},
		{"ErrPayloadTooLarge", ErrPayloadTooLarge},
		{"ErrNotJSON", ErrNotJSON},
		{"ErrEventDisabled", ErrEventDisabled},
		{"ErrMissingBrokers", ErrMissingBrokers},
		{"ErrInvalidCompression", ErrInvalidCompression},
		{"ErrInvalidAcks", ErrInvalidAcks},
	}

	infraCases := []struct {
		name string
		err  error
	}{
		{"ClassTopicNotFound", &EmitError{Class: ClassTopicNotFound}},
		{"ClassBrokerUnavailable", &EmitError{Class: ClassBrokerUnavailable}},
		{"ClassNetworkTimeout", &EmitError{Class: ClassNetworkTimeout}},
		{"ClassContextCanceled", &EmitError{Class: ClassContextCanceled}},
		{"ClassBrokerOverloaded", &EmitError{Class: ClassBrokerOverloaded}},
		{"ErrEmitterClosed", ErrEmitterClosed},
		{"unrelated error", errors.New("some unrelated error")},
		{"nil", nil},
	}

	for _, tc := range callerCases {
		t.Run("caller/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if !IsCallerError(tc.err) {
				t.Errorf("IsCallerError(%v) = false; want true", tc.err)
			}
		})
	}

	for _, tc := range infraCases {
		t.Run("infra/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if IsCallerError(tc.err) {
				t.Errorf("IsCallerError(%v) = true; want false", tc.err)
			}
		})
	}
}

// TestEmitError_ErrorRedactsCredentials asserts that the Error() method
// passes the message through sanitizeBrokerURL before surfacing to callers
// or logs (DX-B06).
func TestEmitError_ErrorRedactsCredentials(t *testing.T) {
	t.Parallel()

	cause := errors.New("dial sasl://user:hunter2@broker:9092 failed")
	ee := &EmitError{
		ResourceType: "transaction",
		EventType:    "created",
		TenantID:     "t-123",
		Topic:        "lerian.streaming.transaction.created",
		Class:        ClassBrokerUnavailable,
		Cause:        cause,
	}

	got := ee.Error()

	if strings.Contains(got, "hunter2") {
		t.Errorf("EmitError.Error() = %q; must not contain credential %q", got, "hunter2")
	}

	if !strings.Contains(got, string(ClassBrokerUnavailable)) {
		t.Errorf("EmitError.Error() = %q; expected class token %q", got, ClassBrokerUnavailable)
	}
}

// TestEmitError_Unwrap verifies errors.Is / errors.Unwrap semantics — the Cause
// is walkable by the standard library unwrap machinery (DX-B02).
func TestEmitError_Unwrap(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("root cause")
	ee := &EmitError{
		Class: ClassNetworkTimeout,
		Cause: sentinel,
	}

	if got := ee.Unwrap(); got != sentinel {
		t.Errorf("EmitError.Unwrap() = %v; want %v", got, sentinel)
	}

	if !errors.Is(ee, sentinel) {
		t.Errorf("errors.Is(ee, sentinel) = false; want true (unwrap chain broken)")
	}
}

// TestEmitError_NilReceiver ensures neither Error() nor Unwrap() panic on a
// nil *EmitError. These shouldn't happen in practice (callers hold the
// value by pointer) but defensive-nil is cheap.
func TestEmitError_NilReceiver(t *testing.T) {
	t.Parallel()

	var ee *EmitError

	if got := ee.Error(); got != "<nil>" {
		t.Errorf("nil.Error() = %q; want \"<nil>\"", got)
	}
	if got := ee.Unwrap(); got != nil {
		t.Errorf("nil.Unwrap() = %v; want nil", got)
	}
}

// TestEmitError_ErrorNoCause exercises the branch where Cause is nil — the
// Error() string must still be well-formed and contain the class.
func TestEmitError_ErrorNoCause(t *testing.T) {
	t.Parallel()

	ee := &EmitError{
		ResourceType: "transaction",
		EventType:    "created",
		Topic:        "lerian.streaming.transaction.created",
		Class:        ClassValidation,
		Cause:        nil,
	}

	got := ee.Error()
	if !strings.Contains(got, string(ClassValidation)) {
		t.Errorf("Error() = %q; want to contain %q", got, ClassValidation)
	}
}

// TestEmitError_AsTarget confirms errors.As correctly extracts *EmitError
// fields from a wrapped error chain (DX-B02).
func TestEmitError_AsTarget(t *testing.T) {
	t.Parallel()

	ee := &EmitError{
		ResourceType: "account",
		EventType:    "updated",
		TenantID:     "t-abc",
		Topic:        "lerian.streaming.account.updated",
		Class:        ClassSerialization,
		Cause:        errors.New("bad payload"),
	}

	// Wrap so we exercise the As machinery.
	wrapped := errors.Join(errors.New("other"), ee)

	var target *EmitError
	if !errors.As(wrapped, &target) {
		t.Fatalf("errors.As(wrapped, &*EmitError) = false; want true")
	}

	if target.ResourceType != "account" {
		t.Errorf("target.ResourceType = %q; want %q", target.ResourceType, "account")
	}
	if target.EventType != "updated" {
		t.Errorf("target.EventType = %q; want %q", target.EventType, "updated")
	}
	if target.TenantID != "t-abc" {
		t.Errorf("target.TenantID = %q; want %q", target.TenantID, "t-abc")
	}
	if target.Class != ClassSerialization {
		t.Errorf("target.Class = %q; want %q", target.Class, ClassSerialization)
	}
}

// TestSentinelErrors_IsMatches confirms errors.Is works for every sentinel.
func TestSentinelErrors_IsMatches(t *testing.T) {
	t.Parallel()

	sentinels := []error{
		ErrMissingTenantID,
		ErrMissingSource,
		ErrEmitterClosed,
		ErrEventDisabled,
		ErrPayloadTooLarge,
		ErrNotJSON,
		ErrMissingBrokers,
		ErrInvalidCompression,
		ErrInvalidAcks,
	}

	for _, s := range sentinels {
		s := s
		t.Run(s.Error(), func(t *testing.T) {
			t.Parallel()
			// Wrap and re-check.
			wrapped := errors.Join(errors.New("outer"), s)
			if !errors.Is(wrapped, s) {
				t.Errorf("errors.Is(wrapped, %v) = false; want true", s)
			}
		})
	}
}
