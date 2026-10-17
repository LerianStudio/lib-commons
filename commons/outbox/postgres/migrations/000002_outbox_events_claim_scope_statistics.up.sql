-- Lets the planner see that a scoped claim's event types are rare among PENDING rows
-- (WithClaimScopedToHandlers), so it uses the event-type index instead of the backlog.
CREATE STATISTICS IF NOT EXISTS outbox_events_status_event_type (mcv) ON status, event_type FROM outbox_events;
