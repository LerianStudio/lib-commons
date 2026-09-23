# Outbox migrations

This directory contains two alternative migration tracks for `outbox_events`:

- `000001_outbox_events_schema.*.sql`: schema-per-tenant strategy (default track in this directory)
- `column/000001_outbox_events_column.*.sql`: column-per-tenant strategy (`tenant_id`)

Use exactly one track for a given deployment topology.

- For schema-per-tenant deployments, point migrations to this directory.
- For column-per-tenant deployments, point migrations to `migrations/column`.

Column track note: primary key is `(tenant_id, id)` to avoid cross-tenant key coupling.

## Optional trace context column

`000002_outbox_trace_context.*.sql` (present in both tracks) adds a nullable
`trace_context JSONB` column. It stores the W3C trace carrier (`traceparent`,
and `tracestate` when present) of the request that produced the event, so the
dispatcher publishes the event inside that trace instead of its own background
trace.

The column is optional in both directions:

- A deployment that never applies this migration keeps working unchanged.
- A deployment that applies it stores and reads carriers only once the
  repository is constructed with `postgres.WithTraceContextColumn("trace_context")`.

Enable the option only after the migration has run against every table the
repository writes to, otherwise every insert fails on an unknown column.
