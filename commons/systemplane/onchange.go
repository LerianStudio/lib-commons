// Change notification dispatch for systemplane Client.
package systemplane

// OnChange registers a callback that is invoked whenever the specified key's
// value changes. The returned function unsubscribes the callback.
//
// Callbacks are invoked sequentially with panic recovery; a slow callback
// backpressures subsequent notifications. The newValue argument is the
// post-change value.
//
// On a nil receiver, OnChange returns a no-op unsubscribe function.
func (c *Client) OnChange(namespace, key string, fn func(newValue any)) (unsubscribe func()) {
	if c == nil {
		return func() {}
	}

	// TODO(phase-5): wire into dispatcher with debounce + panic recovery
	_ = namespace
	_ = key
	_ = fn

	return func() {}
}
