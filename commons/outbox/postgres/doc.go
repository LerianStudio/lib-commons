// Package postgres provides PostgreSQL adapters for outbox repository contracts.
//
// Migration files under migrations/ include two mutually exclusive tracks:
// - schema-per-tenant in migrations/
// - column-per-tenant in migrations/column/
// Choose one strategy per deployment.
//
// SchemaResolver enforces non-empty tenant context by default. Use
// WithAllowEmptyTenant only for explicit single-tenant/public-schema flows.
package postgres
