package consumer

// Stats returns statistics about the consumer including lazy mode metadata.
func (c *MultiTenantConsumer) Stats() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	tenantIDs := make([]string, 0, len(c.tenants))
	for id := range c.tenants {
		tenantIDs = append(tenantIDs, id)
	}

	queueNames := make([]string, 0, len(c.handlers))
	for name := range c.handlers {
		queueNames = append(queueNames, name)
	}

	knownTenantIDs := make([]string, 0, len(c.knownTenants))
	for id := range c.knownTenants {
		knownTenantIDs = append(knownTenantIDs, id)
	}

	// Compute pending tenants (known but not yet active)
	pendingTenantIDs := make([]string, 0)

	for id := range c.knownTenants {
		if _, active := c.tenants[id]; !active {
			pendingTenantIDs = append(pendingTenantIDs, id)
		}
	}

	// Collect degraded tenants from retry state
	degradedTenantIDs := make([]string, 0)

	c.retryState.Range(func(key, value any) bool {
		tenantID, ok := key.(string)
		if !ok {
			return true
		}

		if entry, ok := value.(*retryStateEntry); ok && entry.isDegraded() {
			degradedTenantIDs = append(degradedTenantIDs, tenantID)
		}

		return true
	})

	return Stats{
		ActiveTenants:    len(c.tenants),
		TenantIDs:        tenantIDs,
		RegisteredQueues: queueNames,
		Closed:           c.closed,
		ConnectionMode:   connectionMode(c.config.EagerStart),
		KnownTenants:     len(c.knownTenants),
		KnownTenantIDs:   knownTenantIDs,
		PendingTenants:   len(pendingTenantIDs),
		PendingTenantIDs: pendingTenantIDs,
		DegradedTenants:  degradedTenantIDs,
	}
}

func connectionMode(eagerStart bool) string {
	if eagerStart {
		return "eager"
	}

	return "lazy"
}
