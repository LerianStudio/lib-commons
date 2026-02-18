# Migration Guide: Multi-Tenant Consumer v2 to v3

This guide covers the breaking changes introduced in lib-commons v3.0.0 for the multi-tenant consumer.

## Summary of Changes

The `MultiTenantConsumer` now operates in **lazy mode** by default. This means:

- `Run()` discovers tenants but does NOT start consumers (startup time < 1 second)
- Consumers are spawned **on-demand** via `EnsureConsumerStarted()`
- Connection failures use **exponential backoff** (5s, 10s, 20s, 40s max)
- Tenants are marked as **degraded** after 3 consecutive failures
- `Stats()` returns enhanced metadata (ConnectionMode, KnownTenants, PendingTenants, DegradedTenants)

## Breaking Changes

### 1. MultiTenantConsumerStats has new fields

**Before (v2):**

```go
type MultiTenantConsumerStats struct {
    ActiveTenants    int      `json:"activeTenants"`
    TenantIDs        []string `json:"tenantIds"`
    RegisteredQueues []string `json:"registeredQueues"`
    Closed           bool     `json:"closed"`
}
```

**After (v3):**

```go
type MultiTenantConsumerStats struct {
    ActiveTenants    int      `json:"activeTenants"`
    TenantIDs        []string `json:"tenantIds"`
    RegisteredQueues []string `json:"registeredQueues"`
    Closed           bool     `json:"closed"`
    ConnectionMode   string   `json:"connectionMode"`
    KnownTenants     int      `json:"knownTenants"`
    KnownTenantIDs   []string `json:"knownTenantIds"`
    PendingTenants   int      `json:"pendingTenants"`
    PendingTenantIDs []string `json:"pendingTenantIds"`
    DegradedTenants  []string `json:"degradedTenants"`
}
```

**Action required:** If your code structurally compares or unmarshals `MultiTenantConsumerStats`, update it to handle the new fields.

### 2. Run() no longer starts consumers

**Before (v2):** `Run()` discovered tenants and immediately started consumer goroutines for each.

**After (v3):** `Run()` discovers tenants (populates `knownTenants`) but does NOT start consumers. You must call `EnsureConsumerStarted()` to spawn consumers on-demand.

**Action required:** Add `EnsureConsumerStarted()` calls at the point where your service needs to start consuming for a tenant. Common integration points:

```go
// Example: In a message router that receives tenant-specific triggers
func (r *Router) HandleTenantMessage(ctx context.Context, tenantID string) {
    // Ensure the consumer is running for this tenant
    r.consumer.EnsureConsumerStarted(ctx, tenantID)
}
```

### 3. Connection retry behavior changed

**Before (v2):** Fixed 5-second retry delay on all connection failures.

**After (v3):** Exponential backoff per tenant: 5s, 10s, 20s, 40s (capped). Tenants are marked degraded after 3 consecutive failures.

**Action required:** No code changes needed. The new behavior is backward-compatible in terms of API. Monitor the `IsDegraded()` method or `Stats().DegradedTenants` for tenant health visibility.

## New Features

### On-Demand Consumer Spawning

```go
// Thread-safe, exactly-once guarantee
consumer.EnsureConsumerStarted(ctx, "tenant-123")
```

### Degraded Tenant Detection

```go
if consumer.IsDegraded("tenant-123") {
    // Handle degraded tenant (e.g., alert, skip, retry later)
}
```

### Enhanced Stats

```go
stats := consumer.Stats()
// stats.ConnectionMode    = "lazy"
// stats.KnownTenants     = 50  (discovered but not necessarily consuming)
// stats.ActiveTenants     = 10  (actually consuming)
// stats.PendingTenants    = 40  (known but not yet consuming)
// stats.DegradedTenants   = ["tenant-x"] (connection failures >= 3)
```

### Metric Constants

```go
// Use these constants when instrumenting with Prometheus
tenantmanager.MetricTenantConnectionsTotal  // "tenant_connections_total"
tenantmanager.MetricTenantConnectionErrors  // "tenant_connection_errors_total"
tenantmanager.MetricTenantConsumersActive   // "tenant_consumers_active"
tenantmanager.MetricTenantMessageProcessed  // "tenant_messages_processed_total"
```

## Recommended Migration Steps

1. Update `go.mod` to use lib-commons v3.0.0
2. Review any code that directly inspects `MultiTenantConsumerStats` fields
3. Add `EnsureConsumerStarted()` calls at your service's message entry points
4. (Optional) Add monitoring for `IsDegraded()` or `Stats().DegradedTenants`
5. Run tests to verify behavior
