// Write path for systemplane Client.
package systemplane

import "context"

// Set writes a new value for the given namespace and key. The value is validated
// against the key's registered validator (if any), persisted to the backing store,
// and subscribers are notified after a successful commit.
//
// Returns [ErrClosed] on a nil receiver, [ErrNotStarted] if Start has not been
// called, [ErrUnknownKey] if the key was not registered, and [ErrValidation] if
// the validator rejects the value.
func (c *Client) Set(ctx context.Context, namespace, key string, value any, actor string) error {
	if c == nil {
		return ErrClosed
	}

	// TODO(phase-5): validate, persist, notify
	_ = ctx
	_ = namespace
	_ = key
	_ = value
	_ = actor

	return nil
}
