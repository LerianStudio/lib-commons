# Outbox migrations

This directory contains two alternative migration tracks for `outbox_events`:

- `000001_outbox_events_schema.*.sql`: schema-per-tenant strategy (default track in this directory)
- `column/000001_outbox_events_column.*.sql`: column-per-tenant strategy (`tenant_id`)

Use exactly one track for a given deployment topology.

- For schema-per-tenant deployments, point migrations to this directory.
- For column-per-tenant deployments, point migrations to `migrations/column`.

Column track note: primary key is `(tenant_id, id)` to avoid cross-tenant key coupling.

`000002_outbox_events_claim_scope_statistics` adds extended statistics so a dispatcher using
`WithClaimScopedToHandlers` keeps claiming in under a millisecond while a large backlog of event
types it does not serve sits in the table. The statistics take effect after the next `ANALYZE`.
