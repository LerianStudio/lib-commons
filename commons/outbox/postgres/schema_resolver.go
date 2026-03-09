package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	"github.com/LerianStudio/lib-commons/v4/commons/outbox"
	libPostgres "github.com/LerianStudio/lib-commons/v4/commons/postgres"
)

const uuidSchemaRegex = "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"

// defaultOutboxTableName is the default table name used by the outbox
// pattern for event persistence and tenant schema discovery.
const defaultOutboxTableName = "outbox_events"

// defaultSchemaSearchPath is the schema used when ApplyTenant receives an
// empty or default-tenant ID with AllowEmptyTenant enabled.
const defaultSchemaSearchPath = "public"

var ErrDefaultTenantIDInvalid = errors.New("default tenant id must be UUID when tenant is required")

type SchemaResolverOption func(*SchemaResolver)

func WithDefaultTenantID(tenantID string) SchemaResolverOption {
	return func(resolver *SchemaResolver) {
		resolver.defaultTenantID = tenantID
	}
}

// WithRequireTenant enforces that every ApplyTenant call receives a non-empty
// tenant ID. This is the default behavior.
func WithRequireTenant() SchemaResolverOption {
	return func(resolver *SchemaResolver) {
		resolver.requireTenant = true
	}
}

// WithAllowEmptyTenant permits ApplyTenant calls with empty tenant IDs.
//
// When an empty tenant ID is received, the transaction's search_path is
// explicitly set to the configured default schema ("public") instead of
// relying on the connection's ambient search_path. This prevents cross-tenant
// leakage when the connection pool routes to a connection whose search_path
// was previously set to a different tenant.
func WithAllowEmptyTenant() SchemaResolverOption {
	return func(resolver *SchemaResolver) {
		resolver.requireTenant = false
	}
}

// WithOutboxTableName sets the outbox table name used to verify schema
// eligibility during DiscoverTenants. Only schemas containing this table
// are returned. Defaults to "outbox_events".
func WithOutboxTableName(tableName string) SchemaResolverOption {
	return func(resolver *SchemaResolver) {
		resolver.outboxTableName = tableName
	}
}

// SchemaResolver applies schema-per-tenant scoping and tenant discovery.
type SchemaResolver struct {
	client          *libPostgres.Client
	defaultTenantID string
	outboxTableName string
	requireTenant   bool
}

func NewSchemaResolver(client *libPostgres.Client, opts ...SchemaResolverOption) (*SchemaResolver, error) {
	if client == nil {
		return nil, ErrConnectionRequired
	}

	resolver := &SchemaResolver{client: client, requireTenant: true, outboxTableName: defaultOutboxTableName}

	for _, opt := range opts {
		if opt != nil {
			opt(resolver)
		}
	}

	resolver.defaultTenantID = strings.TrimSpace(resolver.defaultTenantID)
	if resolver.defaultTenantID != "" && resolver.requireTenant && !libCommons.IsUUID(resolver.defaultTenantID) {
		return nil, ErrDefaultTenantIDInvalid
	}

	resolver.outboxTableName = strings.TrimSpace(resolver.outboxTableName)
	if resolver.outboxTableName == "" {
		resolver.outboxTableName = defaultOutboxTableName
	}

	return resolver, nil
}

func (resolver *SchemaResolver) RequiresTenant() bool {
	if resolver == nil {
		return true
	}

	return resolver.requireTenant
}

// ApplyTenant scopes the current transaction to tenant search_path.
//
// Security invariant: tenantID must remain UUID-validated and identifier-quoted
// before query construction. This method intentionally relies on both checks to
// keep dynamic search_path assignment safe.
//
// When tenantID is empty or matches the configured default tenant (with
// AllowEmptyTenant enabled), the search_path is explicitly set to the default
// schema ("public") to prevent queries from running against a stale
// connection-level search_path left by a previous tenant operation.
func (resolver *SchemaResolver) ApplyTenant(ctx context.Context, tx *sql.Tx, tenantID string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if resolver == nil {
		return ErrConnectionRequired
	}

	if tx == nil {
		return ErrTransactionRequired
	}

	tenantID = strings.TrimSpace(tenantID)

	if tenantID == "" {
		if resolver.requireTenant {
			return fmt.Errorf("schema resolver: %w", outbox.ErrTenantIDRequired)
		}

		// Explicitly set search_path to the default schema instead of no-oping.
		// This prevents cross-tenant leakage when the pooled connection retains
		// a search_path from a previous tenant transaction.
		return resolver.setDefaultSearchPath(ctx, tx)
	}

	if tenantID == resolver.defaultTenantID && !resolver.requireTenant {
		// Even for the default tenant, explicitly set the search_path to avoid
		// inheriting a stale tenant-scoped path from the connection pool.
		return resolver.setDefaultSearchPath(ctx, tx)
	}

	if !libCommons.IsUUID(tenantID) {
		return errors.New("invalid tenant id format")
	}

	query := "SET LOCAL search_path TO " + quoteIdentifier(tenantID) + ", public" // #nosec G202 -- tenantID is UUID-validated; quoteIdentifier escapes the identifier
	if _, err := tx.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("set search_path: %w", err)
	}

	return nil
}

// setDefaultSearchPath explicitly sets the transaction search_path to the
// default schema. This is used when no tenant-specific schema is needed,
// ensuring the query doesn't run against a stale search_path.
func (resolver *SchemaResolver) setDefaultSearchPath(ctx context.Context, tx *sql.Tx) error {
	query := "SET LOCAL search_path TO " + quoteIdentifier(defaultSchemaSearchPath) // #nosec G202 -- constant string "public"; quoteIdentifier escapes the identifier
	if _, err := tx.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("set default search_path: %w", err)
	}

	return nil
}

// DiscoverTenants returns tenants by inspecting UUID-shaped schema names
// that contain the configured outbox table (default: "outbox_events").
//
// Only schemas where the outbox table actually exists are returned, preventing
// false positives from empty or unrelated UUID-shaped schemas. The configured
// default tenant is NOT injected unless it was actually found in the database.
func (resolver *SchemaResolver) DiscoverTenants(ctx context.Context) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if resolver == nil || resolver.client == nil {
		return nil, ErrConnectionRequired
	}

	db, err := resolver.primaryDB(ctx)
	if err != nil {
		return nil, err
	}

	// Join pg_namespace with information_schema.tables to verify the outbox
	// table exists in each UUID-shaped schema before returning it as a tenant.
	query := `SELECT n.nspname
		FROM pg_namespace n
		INNER JOIN information_schema.tables t
			ON t.table_schema = n.nspname
			AND t.table_name = $2
		WHERE n.nspname ~* $1` // #nosec G202 -- parameterized query; no dynamic identifiers

	rows, err := db.QueryContext(ctx, query, uuidSchemaRegex, resolver.outboxTableName)
	if err != nil {
		return nil, fmt.Errorf("querying tenant schemas: %w", err)
	}
	defer rows.Close()

	tenants := make([]string, 0)

	for rows.Next() {
		var tenant string
		if scanErr := rows.Scan(&tenant); scanErr != nil {
			return nil, fmt.Errorf("scanning tenant schema: %w", scanErr)
		}

		tenants = append(tenants, tenant)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating tenant schemas: %w", err)
	}

	return tenants, nil
}

func (resolver *SchemaResolver) primaryDB(ctx context.Context) (*sql.DB, error) {
	return resolvePrimaryDB(ctx, resolver.client)
}
