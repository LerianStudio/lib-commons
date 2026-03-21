# Outbox migrations

This directory contains two alternative migration tracks for `outbox_events`:

- `000001_outbox_events_schema.*.sql`: schema-per-tenant strategy (default track in this directory)
- `column/000001_outbox_events_column.*.sql`: column-per-tenant strategy (`tenant_id`)

Use exactly one track for a given deployment topology.

- For schema-per-tenant deployments, point migrations to this directory.
- For column-per-tenant deployments, point migrations to `migrations/column`.

Column track note: primary key is `(tenant_id, id)` to avoid cross-tenant key coupling.
