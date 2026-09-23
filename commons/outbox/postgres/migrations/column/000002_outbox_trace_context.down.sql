ALTER TABLE outbox_events
    DROP COLUMN IF EXISTS trace_context;
