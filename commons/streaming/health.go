package streaming

import "fmt"

// HealthState classifies a Producer's readiness in three discrete buckets,
// matching TRD §7.4. The zero value is Healthy — so a default-initialized
// HealthError conveys "healthy with an attached cause", which would be a
// programming error; callers always construct HealthError with a specific
// state.
type HealthState int

// Health states. Healthy is the default (zero value); Degraded and Down are
// the error-carrying states.
//
//   - Healthy: Producer is fully operational; broker reachable, circuit
//     closed, not shutting down.
//   - Degraded: Broker unreachable (or slow) but the Producer can still
//     durable-queue via outbox fallback. Persistence is guaranteed; latency
//     may be higher than usual.
//   - Down: Both broker and outbox are unreachable. Messages are at risk.
const (
	Healthy HealthState = iota
	Degraded
	Down
)

// String renders the HealthState as a short lowercase token suitable for
// logs, metric labels, and span attributes. Matches the Kubernetes-style
// readiness vocabulary.
func (s HealthState) String() string {
	switch s {
	case Healthy:
		return "healthy"
	case Degraded:
		return "degraded"
	case Down:
		return "down"
	default:
		return "unknown"
	}
}

// HealthError is the structured error returned from Emitter.Healthy when the
// producer cannot service new emits. It carries a typed HealthState so the
// caller (typically a /readyz handler) can decide how to respond: return
// 503 on Down, 200 with a warning on Degraded, etc.
//
// Fields are unexported; callers use the accessor methods. Cause is walked
// by errors.Is / errors.As via Unwrap.
type HealthError struct {
	state HealthState
	cause error
}

// NewHealthError constructs a HealthError with the given state and underlying
// cause. Callers who need to wrap a broker ping error or outbox write error
// use this constructor. Passing state == Healthy is unusual but permitted;
// it captures "technically an error, but not operationally critical".
func NewHealthError(state HealthState, cause error) *HealthError {
	return &HealthError{state: state, cause: cause}
}

// Error renders a human-readable string. The cause error message, if present,
// passes through sanitizeBrokerURL so SASL credentials and full credentialed
// URLs are redacted before surfacing to logs or HTTP responses.
//
// Nil-receiver safe: returns "<nil>" without panicking.
func (e *HealthError) Error() string {
	if e == nil {
		return "<nil>"
	}

	if e.cause == nil {
		return fmt.Sprintf("streaming: health state=%s", e.state)
	}

	return fmt.Sprintf(
		"streaming: health state=%s: %s",
		e.state,
		sanitizeBrokerURL(e.cause.Error()),
	)
}

// State returns the classified HealthState. Nil-receiver safe: returns
// Healthy (the zero value) so a nil *HealthError degrades into a no-error
// result rather than a panic. Callers should treat nil as "no error".
func (e *HealthError) State() HealthState {
	if e == nil {
		return Healthy
	}

	return e.state
}

// Unwrap returns the underlying cause so errors.Is / errors.As can walk the
// chain. Nil-receiver safe.
func (e *HealthError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.cause
}
