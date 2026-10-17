-- Lets the planner see that a scoped claim's event types are rare among a tenant's PENDING
-- rows (WithClaimScopedToHandlers); tenant_id is included because one tenant's backlog of
-- an unserved type must not be costed by other tenants' mix.
CREATE STATISTICS IF NOT EXISTS outbox_events_tenant_status_event_type (mcv) ON tenant_id, status, event_type FROM outbox_events;
