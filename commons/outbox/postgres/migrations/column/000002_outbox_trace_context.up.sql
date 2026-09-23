-- Optional W3C trace carrier for outbox events (column-per-tenant track).
-- Apply this migration once in the shared schema holding outbox_events.
--
-- The column is nullable and additive: a deployment that does not apply it keeps
-- working, and a deployment that applies it only starts persisting carriers once
-- the repository is built with postgres.WithTraceContextColumn.

ALTER TABLE outbox_events
    ADD COLUMN IF NOT EXISTS trace_context JSONB NULL;
