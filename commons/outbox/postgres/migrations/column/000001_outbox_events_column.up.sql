-- Column-per-tenant outbox_events table template.
-- Apply this migration once in a shared schema.

DO $$ BEGIN
    CREATE TYPE outbox_event_status AS ENUM ('PENDING', 'PROCESSING', 'PUBLISHED', 'FAILED', 'INVALID');
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

CREATE TABLE IF NOT EXISTS outbox_events (
    id UUID NOT NULL,
    tenant_id TEXT NOT NULL,
    event_type VARCHAR(255) NOT NULL,
    aggregate_id UUID NOT NULL,
    payload JSONB NOT NULL,
    status outbox_event_status NOT NULL DEFAULT 'PENDING',
    attempts INT NOT NULL DEFAULT 0,
    published_at TIMESTAMPTZ NULL,
    last_error VARCHAR(512),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS idx_outbox_events_tenant_status_created_at
    ON outbox_events (tenant_id, status, created_at ASC);

CREATE INDEX IF NOT EXISTS idx_outbox_events_tenant_status_updated_at
    ON outbox_events (tenant_id, status, updated_at ASC);

CREATE INDEX IF NOT EXISTS idx_outbox_events_tenant_event_type_status_created_at
    ON outbox_events (tenant_id, event_type, status, created_at ASC);
