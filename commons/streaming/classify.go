package streaming

import (
	"context"
	"errors"
	"net"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// classErrorMapping pairs a sentinel error with the ErrorClass it routes to.
// The classifier walks this table using errors.Is — so wrapped sentinels
// (fmt.Errorf("...%w", kerr.X)) still classify correctly.
//
// Ordering matters: entries appear in the order the TRD §C9 table expects
// precedence. Context cancellation is handled first (before this table)
// because it often manifests under a broker-shaped wrap, and we want caller-
// cancel semantics to win.
var classErrorMapping = []struct {
	sentinel error
	class    ErrorClass
}{
	// Auth: three kerr sentinels.
	{kerr.TopicAuthorizationFailed, ClassAuth},
	{kerr.ClusterAuthorizationFailed, ClassAuth},
	{kerr.SaslAuthenticationFailed, ClassAuth},

	// Topic-not-found: single kerr sentinel.
	{kerr.UnknownTopicOrPartition, ClassTopicNotFound},

	// Serialization faults: payload-shape issues.
	{kerr.MessageTooLarge, ClassSerialization},
	{kerr.InvalidRecord, ClassSerialization},
	{kerr.CorruptMessage, ClassSerialization},

	// Broker-overloaded: throttle / quota / policy.
	{kerr.ThrottlingQuotaExceeded, ClassBrokerOverloaded},
	{kerr.PolicyViolation, ClassBrokerOverloaded},

	// Network timeouts from franz-go.
	{kgo.ErrRecordTimeout, ClassNetworkTimeout},
}

// classifyError maps an error returned by the franz-go client (or a standard
// library transport error, or a sentinel) to one of the eight ErrorClass
// buckets declared in streaming.go. See TRD §C9 for the canonical table.
//
// Resolution order (first match wins):
//
//  1. context.Canceled / context.DeadlineExceeded → ClassContextCanceled
//  2. Every sentinel in classErrorMapping, matched via errors.Is.
//  3. Any wrapped net.Error with Timeout()==true → ClassNetworkTimeout.
//  4. kgo.ErrRecordRetries → recursively classify via errors.Unwrap.
//  5. Default → ClassBrokerUnavailable.
//
// The cyclomatic complexity is intentionally spread across small helpers so
// the main function stays under linter limits.
func classifyError(err error) ErrorClass {
	if err == nil {
		// Called defensively — no error means no class. Callers should not
		// call classifyError on a nil error, but returning a sensible default
		// is cheaper than panicking.
		return ClassBrokerUnavailable
	}

	// Context cancellation takes precedence — callers who canceled should see
	// that reflected regardless of what franz-go wrapped around it.
	if isContextError(err) {
		return ClassContextCanceled
	}

	if class, ok := classifyBySentinel(err); ok {
		return class
	}

	if isNetTimeout(err) {
		return ClassNetworkTimeout
	}

	// Retry exhaustion: unwrap one level and re-classify. franz-go typically
	// joins the original cause onto ErrRecordRetries via errors.Join, so the
	// underlying classifier row picks it up. If the cause is opaque, fall
	// through to broker-unavailable.
	if errors.Is(err, kgo.ErrRecordRetries) {
		return classifyRecordRetries(err)
	}

	// Default: treat as broker-unavailable. Covers dial failures, EOFs,
	// write timeouts, and any franz-go internal that doesn't wrap a more
	// specific cause.
	return ClassBrokerUnavailable
}

// isContextError reports whether err is caused by a canceled or expired
// context. Split out so classifyError can stay simple.
func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// classifyBySentinel walks the classErrorMapping table and returns the first
// matching ErrorClass plus a found-ok flag.
func classifyBySentinel(err error) (ErrorClass, bool) {
	for _, m := range classErrorMapping {
		if errors.Is(err, m.sentinel) {
			return m.class, true
		}
	}

	return "", false
}

// isNetTimeout reports whether err satisfies net.Error and self-reports as
// a timeout. Covers DNS timeouts, TLS handshake timeouts, and read/write
// timeouts that franz-go surfaces wrapped inside its internal errors.
func isNetTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// classifyRecordRetries unwraps a kgo.ErrRecordRetries once and re-runs the
// classifier on the underlying cause. If the cause is nil OR the unwrap
// returned the same error (no real unwrap), falls back to broker-unavailable.
//
// This intermediate helper keeps the recursion boundary obvious: exactly one
// unwrap per ErrRecordRetries layer, then a full re-classify.
func classifyRecordRetries(err error) ErrorClass {
	unwrapped := errors.Unwrap(err)
	if unwrapped == nil {
		return ClassBrokerUnavailable
	}

	// Guard against a pathological wrapper that unwraps to itself.
	if errors.Is(unwrapped, err) && errors.Is(err, unwrapped) {
		return ClassBrokerUnavailable
	}

	return classifyError(unwrapped)
}
