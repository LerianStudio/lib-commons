package streaming

import "context"

// noopEmitter is the NoopEmitter implementation. Zero-state, all methods
// return nil. Returned automatically when Config.Enabled is false or the
// broker list is empty.
type noopEmitter struct{}

// NewNoopEmitter returns an Emitter whose methods are unconditional no-ops.
// Safe for feature-flag-off paths, tests, and environments where no broker
// is available. Emit, Close, and Healthy always return nil. The NoopEmitter
// retains no state and is safe for concurrent use.
//
// Mirrors log.NewNop() and metrics.NewNopFactory() (DX-C05).
func NewNoopEmitter() Emitter {
	return &noopEmitter{}
}

// Emit is a no-op; always returns nil.
func (n *noopEmitter) Emit(_ context.Context, _ Event) error {
	return nil
}

// Close is a no-op; always returns nil.
func (n *noopEmitter) Close() error {
	return nil
}

// Healthy is a no-op; always returns nil.
func (n *noopEmitter) Healthy(_ context.Context) error {
	return nil
}
