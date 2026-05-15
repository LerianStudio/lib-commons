// Package circuitbreaker provides service-level circuit breaker orchestration
// and health-check-driven recovery helpers.
//
// Use NewManager to create and manage per-service breakers, then run calls through
// Manager.Execute so failures are tracked consistently across callers.
//
// # Tenant-aware breakers
//
// NewManager returns a value that satisfies both Manager and TenantAwareManager.
// Manager is the backward-compatible no-tenant/process-wide interface.
// TenantAwareManager adds explicit *ForTenant methods for multi-tenant SaaS
// workloads so one tenant's downstream outage does not open the breaker for
// neighboring tenants in the same pod. This additive shape avoids breaking the
// v5 Manager interface while making tenant isolation opt-in and visible at the
// call site.
//
// Metrics and logs use tenant_hash rather than raw tenant_id. The hash is
// stable enough for diagnostics and noisy-neighbor isolation, but avoids
// leaking tenant identifiers into telemetry backends and keeps label values
// bounded to sanitized, non-reversible strings.
//
// NewPassthroughManager returns the canonical feature-flag kill-switch manager:
// it validates identities and callbacks but emits no breaker metrics and always
// pass-through-executes callbacks.
//
// Optional health-check integration can automatically reset breakers after
// downstream services recover.
package circuitbreaker
