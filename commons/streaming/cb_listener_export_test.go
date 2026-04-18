//go:build unit

package streaming

// circuitState returns the current mirrored breaker state as one of the
// flagCB* constants. Exposed as an unexported method so tests in the same
// package can assert on the listener's effect without reaching into the
// atomic directly.
//
// This is an unexported test-only accessor — not part of the public API. Kept
// in a build-tagged file so the main build doesn't carry dead code that only
// tests exercise.
//
// Nil-safe.
func (p *Producer) circuitState() int32 {
	if p == nil {
		return flagCBClosed
	}

	return p.cbStateFlag.Load()
}
