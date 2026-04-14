package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	libPostgres "github.com/LerianStudio/lib-commons/v5/commons/postgres"
	"golang.org/x/sync/singleflight"
)

// ColumnResolver supports column-per-tenant strategy.
//
// ApplyTenant is a no-op because tenant scoping is handled by SQL WHERE clauses
// in Repository when tenantColumn is configured.
type ColumnResolver struct {
	client       *libPostgres.Client
	tableName    string
	tenantColumn string
	tenantTTL    time.Duration
	cacheMu      sync.RWMutex
	cache        []string
	cacheSet     bool
	cacheUntil   time.Time
	sfGroup      singleflight.Group
}

const defaultTenantDiscoveryTTL = 10 * time.Second

// defaultTenantDiscoveryTimeout caps how long a singleflight tenant-discovery
// query may run. Because context.WithoutCancel strips any parent deadline, an
// explicit timeout prevents unbounded queries from blocking all coalesced callers.
const defaultTenantDiscoveryTimeout = 5 * time.Second

type ColumnResolverOption func(*ColumnResolver)

func WithColumnResolverTableName(tableName string) ColumnResolverOption {
	return func(resolver *ColumnResolver) {
		resolver.tableName = tableName
	}
}

func WithColumnResolverTenantColumn(tenantColumn string) ColumnResolverOption {
	return func(resolver *ColumnResolver) {
		resolver.tenantColumn = tenantColumn
	}
}

func WithColumnResolverTenantDiscoveryTTL(ttl time.Duration) ColumnResolverOption {
	return func(resolver *ColumnResolver) {
		if ttl > 0 {
			resolver.tenantTTL = ttl
		}
	}
}

func NewColumnResolver(client *libPostgres.Client, opts ...ColumnResolverOption) (*ColumnResolver, error) {
	if client == nil {
		return nil, ErrConnectionRequired
	}

	resolver := &ColumnResolver{
		client:       client,
		tableName:    "outbox_events",
		tenantColumn: "tenant_id",
		tenantTTL:    defaultTenantDiscoveryTTL,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(resolver)
		}
	}

	resolver.tableName = strings.TrimSpace(resolver.tableName)
	resolver.tenantColumn = strings.TrimSpace(resolver.tenantColumn)

	if resolver.tableName == "" {
		resolver.tableName = "outbox_events"
	}

	if resolver.tenantColumn == "" {
		resolver.tenantColumn = "tenant_id"
	}

	if err := validateIdentifierPath(resolver.tableName); err != nil {
		return nil, fmt.Errorf("table name: %w", err)
	}

	if err := validateIdentifier(resolver.tenantColumn); err != nil {
		return nil, fmt.Errorf("tenant column: %w", err)
	}

	return resolver, nil
}

func (resolver *ColumnResolver) ApplyTenant(_ context.Context, _ *sql.Tx, _ string) error {
	return nil
}

func (resolver *ColumnResolver) DiscoverTenants(ctx context.Context) ([]string, error) {
	if resolver == nil || resolver.client == nil {
		return nil, ErrConnectionRequired
	}

	if cached, ok := resolver.cachedTenants(time.Now().UTC()); ok {
		return cached, nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	// Coalesce concurrent cache-miss queries via singleflight to prevent
	// thundering herd on TTL expiry when multiple dispatchers poll tenants.
	result, err, _ := resolver.sfGroup.Do("discover", func() (any, error) {
		// Double-check cache inside singleflight — another caller may have
		// already refreshed it while we were waiting for the flight leader.
		if cached, ok := resolver.cachedTenants(time.Now().UTC()); ok {
			return cached, nil
		}

		// Use a context that inherits values but not cancellation,
		// so first caller's timeout doesn't cascade to coalesced callers.
		// Apply an explicit timeout to prevent unbounded queries when the
		// parent context's deadline was stripped by WithoutCancel.
		sfCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultTenantDiscoveryTimeout)
		defer cancel()

		return resolver.queryTenants(sfCtx)
	})
	if err != nil {
		return nil, err
	}

	tenants, ok := result.([]string)
	if !ok {
		return nil, fmt.Errorf("unexpected type from singleflight: got %T, expected []string", result)
	}

	return tenants, nil
}

func (resolver *ColumnResolver) queryTenants(ctx context.Context) ([]string, error) {
	db, err := resolver.primaryDB(ctx)
	if err != nil {
		return nil, err
	}

	table := quoteIdentifierPath(resolver.tableName)
	column := quoteIdentifier(resolver.tenantColumn)

	query := "SELECT DISTINCT " + column + " FROM " + table + // #nosec G202 -- table/column names validated at construction via validateIdentifier/validateIdentifierPath; quote functions escape identifiers
		" WHERE status IN ($1, $2, $3) AND " + column + " IS NOT NULL ORDER BY " + column

	rows, err := db.QueryContext(
		ctx,
		query,
		outbox.OutboxStatusPending,
		outbox.OutboxStatusFailed,
		outbox.OutboxStatusProcessing,
	)
	if err != nil {
		return nil, fmt.Errorf("querying distinct tenant ids: %w", err)
	}
	defer rows.Close()

	tenants := make([]string, 0)

	for rows.Next() {
		var tenant string
		if scanErr := rows.Scan(&tenant); scanErr != nil {
			return nil, fmt.Errorf("scanning tenant id: %w", scanErr)
		}

		tenant = strings.TrimSpace(tenant)

		if tenant != "" {
			tenants = append(tenants, tenant)
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating tenant ids: %w", err)
	}

	resolver.storeCachedTenants(tenants, time.Now().UTC())

	return tenants, nil
}

// RequiresTenant returns true because column-per-tenant strategy always requires
// a tenant ID to scope queries via WHERE clauses.
func (resolver *ColumnResolver) RequiresTenant() bool {
	return true
}

func (resolver *ColumnResolver) TenantColumn() string {
	if resolver == nil {
		return ""
	}

	return resolver.tenantColumn
}

func (resolver *ColumnResolver) primaryDB(ctx context.Context) (*sql.DB, error) {
	return resolvePrimaryDB(ctx, resolver.client)
}

func (resolver *ColumnResolver) cachedTenants(now time.Time) ([]string, bool) {
	if resolver.tenantTTL <= 0 {
		return nil, false
	}

	resolver.cacheMu.RLock()
	defer resolver.cacheMu.RUnlock()

	if !resolver.cacheSet || !now.Before(resolver.cacheUntil) {
		return nil, false
	}

	return append([]string(nil), resolver.cache...), true
}

func (resolver *ColumnResolver) storeCachedTenants(tenants []string, now time.Time) {
	if resolver.tenantTTL <= 0 {
		return
	}

	resolver.cacheMu.Lock()
	defer resolver.cacheMu.Unlock()

	resolver.cache = append([]string(nil), tenants...)
	resolver.cacheSet = true
	resolver.cacheUntil = now.Add(resolver.tenantTTL)
}
