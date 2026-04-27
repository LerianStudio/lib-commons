//go:build unit

package streaming

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// fakeNetTimeoutErr is a minimal net.Error implementation whose Timeout()
// returns true. Used to exercise the net.Error path in classifyError
// without coupling the test to a specific stdlib error.
type fakeNetTimeoutErr struct{}

func (fakeNetTimeoutErr) Error() string   { return "fake network timeout" }
func (fakeNetTimeoutErr) Timeout() bool   { return true }
func (fakeNetTimeoutErr) Temporary() bool { return false }

// fakeNetNonTimeoutErr is a net.Error whose Timeout() returns false. Used to
// prove that a non-timeout net.Error falls through to the default bucket.
type fakeNetNonTimeoutErr struct{}

func (fakeNetNonTimeoutErr) Error() string   { return "fake network non-timeout" }
func (fakeNetNonTimeoutErr) Timeout() bool   { return false }
func (fakeNetNonTimeoutErr) Temporary() bool { return false }

// TestClassifyError_Table exercises every row of the TRD §C9 classifier
// mapping table in a single sweep. Each row is a (name, inputErr, wantClass)
// tuple; we build a sub-test for each so failures localize cleanly.
func TestClassifyError_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want ErrorClass
	}{
		{"TopicAuthorizationFailed → auth", kerr.TopicAuthorizationFailed, ClassAuth},
		{"ClusterAuthorizationFailed → auth", kerr.ClusterAuthorizationFailed, ClassAuth},
		{"SaslAuthenticationFailed → auth", kerr.SaslAuthenticationFailed, ClassAuth},

		{"UnknownTopicOrPartition → topic_not_found", kerr.UnknownTopicOrPartition, ClassTopicNotFound},

		{"MessageTooLarge → serialization", kerr.MessageTooLarge, ClassSerialization},
		{"InvalidRecord → serialization", kerr.InvalidRecord, ClassSerialization},
		{"CorruptMessage → serialization", kerr.CorruptMessage, ClassSerialization},

		{"context.Canceled → context_canceled", context.Canceled, ClassContextCanceled},
		{"context.DeadlineExceeded → context_canceled", context.DeadlineExceeded, ClassContextCanceled},

		{"ThrottlingQuotaExceeded → broker_overloaded", kerr.ThrottlingQuotaExceeded, ClassBrokerOverloaded},
		{"PolicyViolation → broker_overloaded", kerr.PolicyViolation, ClassBrokerOverloaded},

		{"kgo.ErrRecordTimeout → network_timeout", kgo.ErrRecordTimeout, ClassNetworkTimeout},
		{"net.Error with Timeout=true → network_timeout", fakeNetTimeoutErr{}, ClassNetworkTimeout},

		{"net.Error with Timeout=false → broker_unavailable", fakeNetNonTimeoutErr{}, ClassBrokerUnavailable},
		{"kgo.ErrClientClosed → broker_unavailable", kgo.ErrClientClosed, ClassBrokerUnavailable},
		{"opaque error → broker_unavailable", errors.New("something weird"), ClassBrokerUnavailable},
		{"nil → broker_unavailable (defensive)", nil, ClassBrokerUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyError(tt.err); got != tt.want {
				t.Errorf("classifyError(%v) = %q; want %q", tt.err, got, tt.want)
			}
		})
	}
}

// TestClassifyError_Wrapped_Unwrap exercises wrapping machinery: a kerr
// sentinel wrapped via fmt.Errorf should still classify correctly, because
// classifyError uses errors.Is.
func TestClassifyError_Wrapped_Unwrap(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("produce attempt failed: %w", kerr.MessageTooLarge)

	if got := classifyError(wrapped); got != ClassSerialization {
		t.Errorf("classifyError(wrapped MessageTooLarge) = %q; want %q", got, ClassSerialization)
	}
}

// TestClassifyError_RecordRetries_Recurses covers the retry-exhaustion branch.
// franz-go returns kgo.ErrRecordRetries with the underlying cause joined in;
// classifyError must unwrap once and reuse the row for the cause.
func TestClassifyError_RecordRetries_Recurses(t *testing.T) {
	t.Parallel()

	// errors.Join wraps two errors; errors.Unwrap on the Joined value
	// returns the first one via Unwrap() []error semantics — but
	// classifyError calls errors.Is for each specific sentinel BEFORE
	// falling into the ErrRecordRetries branch, so a direct Is(err,
	// MessageTooLarge) inside the wrap chain should trigger the
	// serialization row directly.
	joined := errors.Join(kgo.ErrRecordRetries, kerr.MessageTooLarge)

	if got := classifyError(joined); got != ClassSerialization {
		t.Errorf("classifyError(join(ErrRecordRetries, MessageTooLarge)) = %q; want %q",
			got, ClassSerialization)
	}
}

// TestClassifyError_RecordRetries_Opaque covers the path where the joined
// cause is opaque (just ErrRecordRetries alone). Should fall back to
// broker_unavailable.
func TestClassifyError_RecordRetries_Opaque(t *testing.T) {
	t.Parallel()

	if got := classifyError(kgo.ErrRecordRetries); got != ClassBrokerUnavailable {
		t.Errorf("classifyError(ErrRecordRetries alone) = %q; want %q",
			got, ClassBrokerUnavailable)
	}
}

// TestClassifyError_ContextPrecedence: a context.Canceled wrapped inside
// something that could otherwise look like broker_unavailable must still
// classify as context_canceled. Context precedence is explicit in the
// classifier implementation to keep shutdown paths clean.
func TestClassifyError_ContextPrecedence(t *testing.T) {
	t.Parallel()

	// Wrap context.Canceled inside something broker-shaped.
	wrapped := fmt.Errorf("dial failed: %w", context.Canceled)

	if got := classifyError(wrapped); got != ClassContextCanceled {
		t.Errorf("classifyError(dial+Canceled) = %q; want %q", got, ClassContextCanceled)
	}
}

// TestClassifyError_NetTimeout_StdLib exercises the real net package path:
// a net.DialTimeout-style error wrapping an *net.OpError with a timeout
// flag. classifyError should route it to network_timeout.
func TestClassifyError_NetTimeout_StdLib(t *testing.T) {
	t.Parallel()

	// net.OpError satisfies net.Error; fake the Timeout flag via a tailored
	// error struct (stdlib requires a real OS syscall to produce one
	// naturally).
	opErr := &net.OpError{
		Op:     "dial",
		Net:    "tcp",
		Source: nil,
		Addr:   nil,
		Err:    fakeNetTimeoutErr{},
	}

	if got := classifyError(opErr); got != ClassNetworkTimeout {
		t.Errorf("classifyError(net.OpError wrapping timeout) = %q; want %q",
			got, ClassNetworkTimeout)
	}
}

// TestClassifyError_DeadlineExceededThroughTimeoutFace verifies that
// context.DeadlineExceeded takes the context_canceled row even though
// context.deadlineExceededError also satisfies net.Error with Timeout=true.
// Our classifier's ordering puts context-Is above net.Error-As specifically
// to keep this precedence.
func TestClassifyError_DeadlineExceededThroughTimeoutFace(t *testing.T) {
	t.Parallel()

	// Wrap DeadlineExceeded so the test is non-trivial (unwrapping required).
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	if got := classifyError(ctx.Err()); got != ClassContextCanceled {
		t.Errorf("classifyError(expired ctx err) = %q; want %q", got, ClassContextCanceled)
	}
}
