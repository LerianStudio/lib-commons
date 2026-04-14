// Change notification dispatch for systemplane Client.
package systemplane

import (
	"context"
	"sync"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
)

// OnChange registers a callback that is invoked whenever the specified key's
// value changes via the backend changefeed. The returned function unsubscribes
// the callback; it is safe to call multiple times.
//
// Callbacks are invoked sequentially with panic recovery; a panicking callback
// does not prevent subsequent subscribers from being notified. The newValue
// argument is the post-change value.
//
// On a nil receiver or nil fn, OnChange returns a no-op unsubscribe function.
// An unregistered key also returns a no-op (silent tolerance — the caller may
// have registered late or in the wrong order).
func (c *Client) OnChange(namespace, key string, fn func(newValue any)) (unsubscribe func()) {
	noop := func() {}

	if c == nil || fn == nil {
		return noop
	}

	nk := nskey{Namespace: namespace, Key: key}

	// Silent tolerance for unregistered keys.
	c.registryMu.RLock()
	_, registered := c.registry[nk]
	c.registryMu.RUnlock()

	if !registered {
		c.logger.Log(context.Background(), log.LevelDebug, "OnChange called for unregistered key, returning no-op",
			log.String("namespace", namespace),
			log.String("key", key),
		)

		return noop
	}

	id := c.nextSubID.Add(1)

	sub := subscription{
		id: id,
		fn: fn,
	}

	c.subsMu.Lock()
	c.subscribers[nk] = append(c.subscribers[nk], sub)
	c.subsMu.Unlock()

	// The unsubscribe function removes this subscription by id. Safe to call
	// concurrently and multiple times — sync.Once guarantees exactly-once execution.
	var once sync.Once

	return func() {
		once.Do(func() {
			c.subsMu.Lock()
			defer c.subsMu.Unlock()

			subs := c.subscribers[nk]
			for i, s := range subs {
				if s.id == id {
					c.subscribers[nk] = append(subs[:i], subs[i+1:]...)
					return
				}
			}
		})
	}
}
