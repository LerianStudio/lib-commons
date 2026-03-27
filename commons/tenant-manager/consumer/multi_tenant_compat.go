// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"

	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	tmrabbitmq "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/rabbitmq"
)

// NewMultiTenantConsumerWithRabbitMQ creates a MultiTenantConsumer with the
// RabbitMQ manager provided as a positional parameter. This preserves the
// original v4 constructor signature (pre-#396) for backward compatibility.
//
// Deprecated: Use NewMultiTenantConsumerWithError(config, logger, opts...) with
// WithRabbitMQ() option. The rabbitmq parameter moved to WithRabbitMQ().
func NewMultiTenantConsumerWithRabbitMQ(
	rabbitmq *tmrabbitmq.Manager,
	config MultiTenantConfig,
	logger libLog.Logger,
	opts ...Option,
) (*MultiTenantConsumer, error) {
	allOpts := make([]Option, 0, len(opts)+1)
	if rabbitmq != nil {
		allOpts = append(allOpts, WithRabbitMQ(rabbitmq))
	}

	allOpts = append(allOpts, opts...)

	return NewMultiTenantConsumerWithError(config, logger, allOpts...)
}

// NewMultiTenantConsumerWithRedis creates a MultiTenantConsumer with the
// RabbitMQ manager and Redis client provided as positional parameters.
// This preserves the intermediate v4 signature (post-#396, pre-#402) for
// backward compatibility.
//
// Deprecated: Use NewMultiTenantConsumerWithError(config, logger, opts...) with
// WithRabbitMQ() option instead. This function exists for backward compatibility
// with existing v4 callers and will be removed in a future major version.
func NewMultiTenantConsumerWithRedis(
	rabbitmq *tmrabbitmq.Manager,
	_ any, // redisClient accepted and silently ignored for backward compat
	config MultiTenantConfig,
	logger libLog.Logger,
	opts ...Option,
) (*MultiTenantConsumer, error) {
	allOpts := make([]Option, 0, len(opts)+1)
	if rabbitmq != nil {
		allOpts = append(allOpts, WithRabbitMQ(rabbitmq))
	}

	allOpts = append(allOpts, opts...)

	// redisClient is no longer used by the consumer (listener is external);
	// the parameter is accepted and silently ignored for backward compatibility.
	return NewMultiTenantConsumerWithError(config, logger, allOpts...)
}

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

	c.dispatcher.SetOnTenantRemoved(func(ctx context.Context, tenantID string) {
		c.mu.Lock()
		if cancel, ok := c.tenants[tenantID]; ok {
			cancel()
			delete(c.tenants, tenantID)
		}

		delete(c.knownTenants, tenantID)
		c.mu.Unlock()
	})

	// Ensure the consumer uses the same cache as the dispatcher.
	if c.dispatcher.Cache() != nil {
		c.cache = c.dispatcher.Cache()
	}
}
