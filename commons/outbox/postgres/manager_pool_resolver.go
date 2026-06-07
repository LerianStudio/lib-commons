package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	tmclient "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/client"
	tmcore "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	tmpostgres "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/postgres"
	"github.com/bxcodec/dbresolver/v2"
)

var (
	// ErrManagerRequired is returned when a nil tenant-manager Manager is passed
	// to NewManagerPoolResolver.
	ErrManagerRequired = errors.New("tenant-manager manager is required")

	// ErrServiceRequired is returned when an empty service name is passed to
	// NewManagerPoolResolver.
	ErrServiceRequired = errors.New("service name is required")

	// ErrDefaultTenantIDRequired is returned when an empty default tenant ID is
	// passed to NewManagerPoolResolver. The default tenant is the platform
	// tenant that lives in the root pool rather than in Tenant Manager.
	ErrDefaultTenantIDRequired = errors.New("default tenant id is required")

	// ErrInvalidDefaultTenantID is returned when the default tenant ID does not
	// satisfy tenant-manager/core.IsValidTenantID.
	ErrInvalidDefaultTenantID = errors.New("invalid default tenant id")
)

// tenantPoolConnection is the minimal surface of *tmpostgres.PostgresConnection
// used by the adapter. It exists to allow unit tests to stub connections.
type tenantPoolConnection interface {
	GetDB() (dbresolver.DB, error)
}

// tenantConnectionManager is the minimal surface of *tmpostgres.Manager used by
// the adapter. *managerAdapter bridges the concrete Manager to this seam.
type tenantConnectionManager interface {
	GetConnection(ctx context.Context, tenantID string) (tenantPoolConnection, error)
}

// tenantServiceLister is the minimal surface of *tmclient.Client used by the
// adapter to enumerate active tenants for a service.
type tenantServiceLister interface {
	listTenantIDs(ctx context.Context, service string) ([]string, error)
}

// managerAdapter bridges *tmpostgres.Manager to tenantConnectionManager.
type managerAdapter struct {
	mgr *tmpostgres.Manager
}

func (a managerAdapter) GetConnection(ctx context.Context, tenantID string) (tenantPoolConnection, error) {
	conn, err := a.mgr.GetConnection(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	if conn == nil {
		return nil, ErrNoPrimaryDB
	}

	return conn, nil
}

// clientAdapter bridges *tmclient.Client to tenantServiceLister.
type clientAdapter struct {
	client *tmclient.Client
}

func (a clientAdapter) listTenantIDs(ctx context.Context, service string) ([]string, error) {
	summaries, err := a.client.GetActiveTenantsByService(ctx, service)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(summaries))

	for _, summary := range summaries {
		if summary == nil {
			continue
		}

		id := strings.TrimSpace(summary.ID)
		if id == "" {
			continue
		}

		ids = append(ids, id)
	}

	return ids, nil
}

// ManagerPoolResolver resolves per-tenant pools through a tenant-manager
// Manager, satisfying outbox.TenantPoolResolver.
//
// The default tenant is the platform tenant: it is NOT registered in Tenant
// Manager, so its pool is resolved from the root client, never via a Manager
// lookup. Every other tenant is resolved through the Manager, whose own LRU
// cache absorbs the cost of re-resolving on every call. This adapter
// intentionally does not cache *sql.DB across cycles, because the Manager may
// evict and rebuild a tenant's pool at any time.
type ManagerPoolResolver struct {
	manager         tenantConnectionManager
	lister          tenantServiceLister
	rootClient      resolverProvider
	service         string
	defaultTenantID string
}

// NewManagerPoolResolver builds a ManagerPoolResolver.
//
// mgr resolves per-tenant pools; client enumerates active tenants for service;
// rootClient resolves the platform default tenant's pool (defaultTenantID),
// which is never looked up in Tenant Manager.
func NewManagerPoolResolver(
	mgr *tmpostgres.Manager,
	client *tmclient.Client,
	rootClient resolverProvider,
	service string,
	defaultTenantID string,
) (*ManagerPoolResolver, error) {
	if mgr == nil {
		return nil, ErrManagerRequired
	}

	if client == nil {
		return nil, ErrManagerRequired
	}

	return newManagerPoolResolver(managerAdapter{mgr: mgr}, clientAdapter{client: client}, rootClient, service, defaultTenantID)
}

// newManagerPoolResolver is the seam-friendly constructor used by tests.
func newManagerPoolResolver(
	mgr tenantConnectionManager,
	lister tenantServiceLister,
	rootClient resolverProvider,
	service string,
	defaultTenantID string,
) (*ManagerPoolResolver, error) {
	if mgr == nil || lister == nil {
		return nil, ErrManagerRequired
	}

	if rootClient == nil {
		return nil, ErrConnectionRequired
	}

	service = strings.TrimSpace(service)
	if service == "" {
		return nil, ErrServiceRequired
	}

	defaultTenantID = strings.TrimSpace(defaultTenantID)
	if defaultTenantID == "" {
		return nil, ErrDefaultTenantIDRequired
	}

	if !tmcore.IsValidTenantID(defaultTenantID) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidDefaultTenantID, defaultTenantID)
	}

	return &ManagerPoolResolver{
		manager:         mgr,
		lister:          lister,
		rootClient:      rootClient,
		service:         service,
		defaultTenantID: defaultTenantID,
	}, nil
}

// PoolForTenant returns the *sql.DB owning tenantID's outbox rows.
func (r *ManagerPoolResolver) PoolForTenant(ctx context.Context, tenantID string) (*sql.DB, error) {
	if r == nil {
		return nil, ErrTenantPoolUnavailable
	}

	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrTenantPoolUnavailable
	}

	// Default (platform) tenant lives in the root pool and is never registered
	// in Tenant Manager. Resolve from the root client directly.
	if tenantID == r.defaultTenantID {
		return resolvePrimaryDB(ctx, r.rootClient)
	}

	conn, err := r.manager.GetConnection(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get tenant connection %q: %w", tenantID, err)
	}

	if conn == nil {
		return nil, ErrNoPrimaryDB
	}

	resolved, err := conn.GetDB()
	if err != nil {
		return nil, fmt.Errorf("get tenant db %q: %w", tenantID, err)
	}

	if resolved == nil {
		return nil, ErrNoPrimaryDB
	}

	primaryDBs := resolved.PrimaryDBs()
	if len(primaryDBs) == 0 || primaryDBs[0] == nil {
		return nil, ErrNoPrimaryDB
	}

	return primaryDBs[0], nil
}

// ListTenants returns active tenant IDs for the service plus the platform
// default tenant (appended when absent), since the default tenant is not
// enumerated by Tenant Manager.
func (r *ManagerPoolResolver) ListTenants(ctx context.Context) ([]string, error) {
	if r == nil {
		return nil, ErrTenantPoolUnavailable
	}

	ids, err := r.lister.listTenantIDs(ctx, r.service)
	if err != nil {
		return nil, fmt.Errorf("list active tenants for service %q: %w", r.service, err)
	}

	tenants := make([]string, 0, len(ids)+1)
	seen := false

	for _, id := range ids {
		if id == r.defaultTenantID {
			seen = true
		}

		tenants = append(tenants, id)
	}

	if !seen {
		tenants = append(tenants, r.defaultTenantID)
	}

	return tenants, nil
}

// compile-time assertion: ManagerPoolResolver satisfies the generic interface.
var _ outbox.TenantPoolResolver = (*ManagerPoolResolver)(nil)
