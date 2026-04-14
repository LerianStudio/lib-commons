// Write path for systemplane Client.
package systemplane

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/internal/store"
)

// Set writes a new value for the given namespace and key. The value is validated
// against the key's registered validator (if any), JSON-marshaled, persisted to
// the backing store, and the in-memory cache is updated immediately for
// same-process read consistency.
//
// Subscribers are NOT fired from Set. The changefeed echo drives OnChange
// notifications, which avoids double-firing and preserves the semantic that
// OnChange observes *backend* state changes.
//
// Returns [ErrClosed] on a nil receiver, [ErrNotStarted] if Start has not been
// called, [ErrUnknownKey] if the key was not registered, and [ErrValidation] if
// the validator rejects the value or the value is not JSON-serializable.
func (c *Client) Set(ctx context.Context, namespace, key string, value any, actor string) error {
	if c == nil || c.closed.Load() {
		return ErrClosed
	}

	if !c.started.Load() {
		return ErrNotStarted
	}

	nk := nskey{Namespace: namespace, Key: key}

	// Look up registration.
	c.registryMu.RLock()
	def, registered := c.registry[nk]
	c.registryMu.RUnlock()

	if !registered {
		return fmt.Errorf("%w: %s/%s", ErrUnknownKey, namespace, key)
	}

	// Validate if a validator is set.
	if def.validator != nil {
		if err := def.validator(value); err != nil {
			return fmt.Errorf("%w: %w", ErrValidation, err)
		}
	}

	// JSON-marshal the value for storage.
	jsonBytes, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: value is not JSON-serializable: %w", ErrValidation, err)
	}

	entry := store.Entry{
		Namespace: namespace,
		Key:       key,
		Value:     jsonBytes,
		UpdatedAt: time.Now(),
		UpdatedBy: actor,
	}

	if err := c.store.Set(ctx, entry); err != nil {
		return err
	}

	// Write-through cache: update immediately so a subsequent Get in the same
	// process sees the new value without waiting for the changefeed roundtrip.
	c.cacheMu.Lock()
	c.cache[nk] = value
	c.cacheMu.Unlock()

	return nil
}
