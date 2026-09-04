// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
)

// wireDispatcherCallbacks attaches the consumer's internal state management
// callbacks to an externally-injected EventDispatcher. This ensures that
// knownTenants and tenant goroutines are managed correctly regardless of
// whether the dispatcher was built internally or injected via WithEventDispatcher.
func (c *MultiTenantConsumer) wireDispatcherCallbacks() {
	c.dispatcher.SetOnTenantAdded(func(ctx context.Context, tenantID string) {
		c.mu.Lock()
		c.knownTenants[tenantID] = true
		c.mu.Unlock()

		c.EnsureConsumerStarted(ctx, tenantID)
	})

	c.dispatcher.SetOnTenantRemoved(func(_ context.Context, tenantID string) {
		c.StopConsumer(tenantID)
	})

	// Ensure the consumer uses the same cache as the dispatcher.
	if c.dispatcher.Cache() != nil {
		c.cache = c.dispatcher.Cache()
	}
}
