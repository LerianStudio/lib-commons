-- Schema-per-tenant outbox_events table template.
-- Apply this migration inside each tenant schema.

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_type t
        INNER JOIN pg_namespace n ON n.oid = t.typnamespace
        WHERE t.typname = 'outbox_event_status'
          AND n.nspname = current_schema()
    ) THEN
        CREATE TYPE outbox_event_status AS ENUM ('PENDING', 'PROCESSING', 'PUBLISHED', 'FAILED', 'INVALID');
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS outbox_events (
    id UUID PRIMARY KEY,
    event_type VARCHAR(255) NOT NULL,
    aggregate_id UUID NOT NULL,
    payload JSONB NOT NULL,
    status outbox_event_status NOT NULL DEFAULT 'PENDING',
    attempts INT NOT NULL DEFAULT 0,
    published_at TIMESTAMPTZ NULL,
    last_error VARCHAR(512),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_outbox_events_status_created_at
    ON outbox_events (status, created_at ASC);

CREATE INDEX IF NOT EXISTS idx_outbox_events_status_updated_at
    ON outbox_events (status, updated_at ASC);

CREATE INDEX IF NOT EXISTS idx_outbox_events_event_type_status_created_at
    ON outbox_events (event_type, status, created_at ASC);
