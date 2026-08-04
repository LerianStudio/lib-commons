//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v6/commons/outbox"
	libPostgres "github.com/LerianStudio/lib-commons/v6/commons/postgres"
	tmcore "github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
)

// Pool-per-tenant integration harness.
//
// Models pool-per-tenant by creating multiple databases inside ONE Postgres
// container (tenant_a, tenant_b, root_db, ...) with a dedicated *sql.DB pool per
// database. A map-backed outbox.TenantPoolResolver routes each tenant to its
// pool. The repository is wired through NewMultiTenantRepository with a
// NoopTenantResolver, so isolation comes purely from the pool (the per-tenant
// outbox table has NO tenant_id column — matching migrations/000001).
//
// The cross-tenant dispatch cycle is driven through the public Dispatcher
// surface, mirroring Dispatcher.dispatchAcrossTenants exactly: enumerate
// repo.ListTenants, then DispatchOnceResult per tenant under a tenant-stamped
// context. A tenant that fails to resolve or whose table is missing is skipped
// without aborting the cycle, just as the unexported loop does.

const (
	poolDefaultTenant = "11111111-1111-1111-1111-111111111111"
	poolTenantA       = "0f0e0d0c-0b0a-4908-8706-050403020100"
	poolTenantB       = "1a2b3c4d-5e6f-4a8b-9c0d-1e2f3a4b5c6d"
	poolTenantC       = "2b3c4d5e-6f7a-4b9c-8d0e-2f3a4b5c6d7e" // deprovisioned: resolver errors
	poolTenantD       = "3c4d5e6f-7a8b-4c0d-9e1f-3a4b5c6d7e8f" // has pool, no outbox table
)

// poolHarness owns the container and the per-database pools.
type poolHarness struct {
	pools   map[string]*sql.DB
	rootDSN string
}

// mapPoolResolver is a test outbox.TenantPoolResolver over a static map of
// tenant -> pool. Unknown tenants fail closed with ErrTenantPoolUnavailable.
// Tenants present in listFn but absent from the pool map are "enumerated but
// unresolvable" (models a deprovisioned tenant).
type mapPoolResolver struct {
	mu        sync.Mutex
	pools     map[string]*sql.DB
	list      []string
	poolCalls map[string]int
}

func newMapPoolResolver(pools map[string]*sql.DB, list []string) *mapPoolResolver {
	return &mapPoolResolver{
		pools:     pools,
		list:      append([]string(nil), list...),
		poolCalls: make(map[string]int),
	}
}

func (r *mapPoolResolver) PoolForTenant(_ context.Context, tenantID string) (*sql.DB, error) {
	r.mu.Lock()
	r.poolCalls[tenantID]++
	r.mu.Unlock()

	db, ok := r.pools[tenantID]
	if !ok || db == nil {
		// Fail closed: never hand back a shared/root pool for an unknown tenant.
		return nil, fmt.Errorf("resolve tenant %q: %w", tenantID, ErrTenantPoolUnavailable)
	}

	return db, nil
}

func (r *mapPoolResolver) ListTenants(_ context.Context) ([]string, error) {
	return append([]string(nil), r.list...), nil
}

func (r *mapPoolResolver) poolForTenantCalls(tenantID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.poolCalls[tenantID]
}

// outboxTableDDL mirrors migrations/000001_outbox_events_schema.up.sql: a
// tenant-column-free outbox_events table, since pool mode uses the pool itself
// as the isolation boundary.
const outboxTableDDL = `
DO $enum$ BEGIN
    CREATE TYPE outbox_event_status AS ENUM ('PENDING','PROCESSING','PUBLISHED','FAILED','INVALID');
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $enum$;

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
`

// newPoolHarness boots one container, creates the requested databases, opens a
// pool per database, and applies the outbox DDL to every database whose name is
// in withTable.
func newPoolHarness(t *testing.T, databases []string, withTable map[string]bool) *poolHarness {
	t.Helper()

	dsn, cleanup := setupOutboxPostgresContainer(t)
	if cleanup != nil {
		t.Cleanup(cleanup)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	adminDB, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = adminDB.Close() })
	require.NoError(t, adminDB.PingContext(ctx))

	pools := make(map[string]*sql.DB, len(databases))

	for _, dbName := range databases {
		// CREATE DATABASE cannot be parameterized; dbName is a test constant.
		_, err := adminDB.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %s", quoteIdentifier(dbName)))
		require.NoError(t, err, "create database %s", dbName)

		dbDSN := replaceDSNDatabase(t, dsn, dbName)

		pool, err := sql.Open("pgx", dbDSN)
		require.NoError(t, err)

		poolName := dbName
		t.Cleanup(func() { _ = pool.Close() })
		require.NoError(t, pool.PingContext(ctx), "ping %s", poolName)

		if withTable[dbName] {
			_, err := pool.ExecContext(ctx, outboxTableDDL)
			require.NoError(t, err, "apply outbox DDL in %s", dbName)
		}

		pools[dbName] = pool
	}

	return &poolHarness{
		pools:   pools,
		rootDSN: replaceDSNDatabase(t, dsn, "root_db"),
	}
}

// replaceDSNDatabase rewrites the database segment of a postgres URL DSN.
func replaceDSNDatabase(t *testing.T, dsn, dbName string) string {
	t.Helper()

	// tcpostgres ConnectionString yields a URL DSN:
	// postgres://user:pass@host:port/dbname?sslmode=disable
	at := -1
	for i := 0; i < len(dsn); i++ {
		if dsn[i] == '@' {
			at = i
			break
		}
	}
	require.GreaterOrEqual(t, at, 0, "DSN missing '@': %s", dsn)

	slash := -1
	for i := at; i < len(dsn); i++ {
		if dsn[i] == '/' {
			slash = i
			break
		}
	}
	require.GreaterOrEqual(t, slash, 0, "DSN missing path '/': %s", dsn)

	query := ""
	for i := slash + 1; i < len(dsn); i++ {
		if dsn[i] == '?' {
			query = dsn[i:]
			break
		}
	}

	return dsn[:slash+1] + dbName + query
}

// newPoolRepo wires NewMultiTenantRepository in pool-per-tenant mode against the
// harness's resolver, with a root client pointing at root_db (required by the
// constructor and initialized() guard; routing still flows through the resolver).
func newPoolRepo(t *testing.T, h *poolHarness, resolver outbox.TenantPoolResolver) *Repository {
	t.Helper()
	t.Setenv("ALLOW_INSECURE_TLS", "true")

	rootClient, err := libPostgres.New(libPostgres.Config{PrimaryDSN: h.rootDSN, ReplicaDSN: h.rootDSN})
	require.NoError(t, err)

	connectCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, rootClient.Connect(connectCtx))
	t.Cleanup(func() { _ = rootClient.Close() })

	repo, err := NewMultiTenantRepository(MultiTenantConfig{
		Client:             rootClient,
		PoolResolver:       resolver,
		MultiTenantEnabled: true,
	})
	require.NoError(t, err)

	return repo
}

// createEventForTenant writes a pending outbox event through the repository write
// path, which routes to the tenant's pool via the resolver.
func createEventForTenant(t *testing.T, repo *Repository, tenantID, eventType string) *outbox.OutboxEvent {
	t.Helper()

	ctx := outbox.ContextWithTenantID(context.Background(), tenantID)
	event, err := outbox.NewOutboxEvent(ctx, eventType, uuid.New(), []byte(`{"ok":true}`))
	require.NoError(t, err)

	created, err := repo.Create(ctx, event)
	require.NoError(t, err)

	return created
}

// statusInDB reads the persisted status directly from a tenant's database pool.
func statusInDB(t *testing.T, pool *sql.DB, id uuid.UUID) (string, bool) {
	t.Helper()

	var status string
	err := pool.QueryRowContext(context.Background(),
		"SELECT status FROM outbox_events WHERE id = $1", id).Scan(&status)
	if err == sql.ErrNoRows {
		return "", false
	}
	require.NoError(t, err)

	return status, true
}

func countInDB(t *testing.T, pool *sql.DB) int {
	t.Helper()

	var n int
	require.NoError(t, pool.QueryRowContext(context.Background(),
		"SELECT count(*) FROM outbox_events").Scan(&n))

	return n
}

func createEventInPool(t *testing.T, pool *sql.DB, tenantID, eventType string) *outbox.OutboxEvent {
	t.Helper()

	ctx := outbox.ContextWithTenantID(context.Background(), tenantID)
	event, err := outbox.NewOutboxEvent(ctx, eventType, uuid.New(), []byte(`{"scope":"module"}`))
	require.NoError(t, err)

	_, err = pool.ExecContext(ctx, `
INSERT INTO outbox_events (
    id, event_type, aggregate_id, payload, status, attempts, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5::outbox_event_status, $6, $7, $8)
`, event.ID, event.EventType, event.AggregateID, event.Payload, event.Status, event.Attempts, event.CreatedAt, event.UpdatedAt)
	require.NoError(t, err)

	return event
}

func integrationTenantConfigWithDatabases(genericDatabase, moduleDatabase string) *tmcore.TenantConfig {
	return &tmcore.TenantConfig{Databases: map[string]tmcore.DatabaseConfig{
		"aaa-generic": {
			PostgreSQL: &tmcore.PostgreSQLConfig{Host: "postgres", Port: 5432, Database: genericDatabase},
		},
		"consignado": {
			PostgreSQL: &tmcore.PostgreSQLConfig{Host: "postgres", Port: 5432, Database: moduleDatabase},
		},
	}}
}

// runDispatcherUntil starts the real cross-tenant dispatcher and stops it once
// the caller-observed database state proves the initial cycle completed.
func runDispatcherUntil(t *testing.T, dispatcher *outbox.Dispatcher, done func() bool) {
	t.Helper()

	runCtx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- dispatcher.RunContext(runCtx, nil)
	}()

	// require.Eventually fails via t.FailNow, which skips the inline
	// cancel/drain below. This cleanup stops the dispatcher and waits for it
	// before t.Cleanup closes the harness pools, so an assertion failure is
	// not followed by unrelated connection errors. The cleanup runs on the
	// test goroutine, so reading drained is race-free.
	drained := false
	t.Cleanup(func() {
		cancel()
		if !drained {
			<-runDone
		}
	})

	require.Eventually(t, done, 10*time.Second, 20*time.Millisecond)
	cancel()
	runErr := <-runDone
	drained = true
	require.NoError(t, runErr)
}

func newPoolDispatcher(t *testing.T, repo *Repository, handlers *outbox.HandlerRegistry) *outbox.Dispatcher {
	t.Helper()

	dispatcher, err := outbox.NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("pool-integration"),
		outbox.WithBatchSize(50),
		outbox.WithPublishMaxAttempts(1),
	)
	require.NoError(t, err)

	return dispatcher
}

// ---- Test 1: cross-DB isolation ----

func TestIntegration_Pool_CrossDatabaseIsolation(t *testing.T) {
	h := newPoolHarness(t,
		[]string{"root_db", "tenant_a", "tenant_b"},
		map[string]bool{"root_db": true, "tenant_a": true, "tenant_b": true},
	)

	resolver := newMapPoolResolver(
		map[string]*sql.DB{
			poolDefaultTenant: h.pools["root_db"],
			poolTenantA:       h.pools["tenant_a"],
			poolTenantB:       h.pools["tenant_b"],
		},
		[]string{poolTenantA, poolTenantB},
	)

	repo := newPoolRepo(t, h, resolver)

	evA := createEventForTenant(t, repo, poolTenantA, "evt.a")
	evB := createEventForTenant(t, repo, poolTenantB, "evt.b")

	// Each event must land only in its own database.
	require.Equal(t, 1, countInDB(t, h.pools["tenant_a"]))
	require.Equal(t, 1, countInDB(t, h.pools["tenant_b"]))
	_, okAinB := statusInDB(t, h.pools["tenant_b"], evA.ID)
	require.False(t, okAinB, "tenant-A event must not appear in tenant-B DB")
	_, okBinA := statusInDB(t, h.pools["tenant_a"], evB.ID)
	require.False(t, okBinA, "tenant-B event must not appear in tenant-A DB")

	// Track which tenant each handler invocation observed.
	var mu sync.Mutex
	seen := map[string][]uuid.UUID{}
	handlers := outbox.NewHandlerRegistry()
	record := func(_ context.Context, event *outbox.OutboxEvent) error {
		mu.Lock()
		seen[event.EventType] = append(seen[event.EventType], event.ID)
		mu.Unlock()
		return nil
	}
	require.NoError(t, handlers.Register("evt.a", record))
	require.NoError(t, handlers.Register("evt.b", record))

	dispatcher := newPoolDispatcher(t, repo, handlers)
	runDispatcherUntil(t, dispatcher, func() bool {
		statusA, okA := statusInDB(t, h.pools["tenant_a"], evA.ID)
		statusB, okB := statusInDB(t, h.pools["tenant_b"], evB.ID)

		return okA && okB && statusA == outbox.OutboxStatusPublished && statusB == outbox.OutboxStatusPublished
	})

	mu.Lock()
	require.Equal(t, []uuid.UUID{evA.ID}, seen["evt.a"], "evt.a handler saw only tenant-A event")
	require.Equal(t, []uuid.UUID{evB.ID}, seen["evt.b"], "evt.b handler saw only tenant-B event")
	mu.Unlock()

	// Both end PUBLISHED in their own DB only.
	statusA, okA := statusInDB(t, h.pools["tenant_a"], evA.ID)
	require.True(t, okA)
	require.Equal(t, outbox.OutboxStatusPublished, statusA)

	statusB, okB := statusInDB(t, h.pools["tenant_b"], evB.ID)
	require.True(t, okB)
	require.Equal(t, outbox.OutboxStatusPublished, statusB)
}

// ---- Test 2: default tenant on root pool ----

func TestIntegration_Pool_DefaultTenantOnRoot(t *testing.T) {
	h := newPoolHarness(t,
		[]string{"root_db", "tenant_a"},
		map[string]bool{"root_db": true, "tenant_a": true},
	)

	resolver := newMapPoolResolver(
		map[string]*sql.DB{
			poolDefaultTenant: h.pools["root_db"],
			poolTenantA:       h.pools["tenant_a"],
		},
		[]string{poolDefaultTenant},
	)

	repo := newPoolRepo(t, h, resolver)

	ev := createEventForTenant(t, repo, poolDefaultTenant, "evt.default")
	require.Equal(t, 1, countInDB(t, h.pools["root_db"]))
	require.Equal(t, 0, countInDB(t, h.pools["tenant_a"]))

	var handled bool
	handlers := outbox.NewHandlerRegistry()
	require.NoError(t, handlers.Register("evt.default", func(_ context.Context, e *outbox.OutboxEvent) error {
		require.Equal(t, ev.ID, e.ID)
		handled = true
		return nil
	}))

	dispatcher := newPoolDispatcher(t, repo, handlers)
	runDispatcherUntil(t, dispatcher, func() bool {
		status, ok := statusInDB(t, h.pools["root_db"], ev.ID)

		return ok && status == outbox.OutboxStatusPublished
	})
	require.True(t, handled)

	status, ok := statusInDB(t, h.pools["root_db"], ev.ID)
	require.True(t, ok)
	require.Equal(t, outbox.OutboxStatusPublished, status)
}

// ---- Test 3: deprovisioned tenant skip ----

func TestIntegration_Pool_DeprovisionedTenantSkipped(t *testing.T) {
	h := newPoolHarness(t,
		[]string{"root_db", "tenant_a"},
		map[string]bool{"root_db": true, "tenant_a": true},
	)

	// tenant-C is enumerated but absent from the pool map -> PoolForTenant errors.
	resolver := newMapPoolResolver(
		map[string]*sql.DB{
			poolTenantA: h.pools["tenant_a"],
		},
		[]string{poolTenantA, poolTenantC},
	)

	repo := newPoolRepo(t, h, resolver)

	evA := createEventForTenant(t, repo, poolTenantA, "evt.a")

	var handledA atomic.Int32
	handlers := outbox.NewHandlerRegistry()
	require.NoError(t, handlers.Register("evt.a", func(_ context.Context, _ *outbox.OutboxEvent) error {
		handledA.Add(1)
		return nil
	}))

	dispatcher := newPoolDispatcher(t, repo, handlers)

	// Tenant-C is skipped while tenant-A still dispatches through the real loop.
	runDispatcherUntil(t, dispatcher, func() bool {
		status, ok := statusInDB(t, h.pools["tenant_a"], evA.ID)

		return ok && status == outbox.OutboxStatusPublished
	})

	require.EqualValues(t, 1, handledA.Load())

	statusA, ok := statusInDB(t, h.pools["tenant_a"], evA.ID)
	require.True(t, ok)
	require.Equal(t, outbox.OutboxStatusPublished, statusA)
}

// ---- Test 4: missing-table skip ----

func TestIntegration_Pool_MissingTableSkipped(t *testing.T) {
	// tenant-D has a real pool but NO outbox table.
	h := newPoolHarness(t,
		[]string{"root_db", "tenant_a", "tenant_d"},
		map[string]bool{"root_db": true, "tenant_a": true, "tenant_d": false},
	)

	resolver := newMapPoolResolver(
		map[string]*sql.DB{
			poolTenantA: h.pools["tenant_a"],
			poolTenantD: h.pools["tenant_d"],
		},
		[]string{poolTenantA, poolTenantD},
	)

	repo := newPoolRepo(t, h, resolver)

	evA := createEventForTenant(t, repo, poolTenantA, "evt.a")

	var handledA atomic.Int32
	handlers := outbox.NewHandlerRegistry()
	require.NoError(t, handlers.Register("evt.a", func(_ context.Context, _ *outbox.OutboxEvent) error {
		handledA.Add(1)
		return nil
	}))

	dispatcher := newPoolDispatcher(t, repo, handlers)
	runDispatcherUntil(t, dispatcher, func() bool {
		status, ok := statusInDB(t, h.pools["tenant_a"], evA.ID)

		return ok && status == outbox.OutboxStatusPublished
	})

	// tenant-D's missing table must be skipped cleanly: no 42P01 surfaced as a
	// dispatch failure and tenant-A remains unaffected.
	require.EqualValues(t, 1, handledA.Load())

	// A full dispatch cycle drives ALL four per-tenant reads through
	// collectEvents: collectPriorityEvents/ListPendingByType, ResetStuckProcessing,
	// ResetForRetry, and ListPending. With the presence guard now wired into every
	// one of them (state_transitions.go ResetForRetry/ResetStuckProcessing mirror
	// ListPending), a missing-table tenant resolves its pool exactly ONCE for the
	// first probe, then every subsequent read in this cycle and the next is served
	// from the 60s presence cache with zero SQL and zero pool resolution.
	//
	// Cycle 1 therefore costs exactly one PoolForTenant call for tenant-D.
	callsAfterCycle1 := resolver.poolForTenantCalls(poolTenantD)
	require.Equal(t, 1, callsAfterCycle1,
		"missing-table tenant must resolve its pool exactly once across all four guarded reads in a cycle")

	// A full second cross-tenant cycle within the TTL must stay clean AND must not
	// re-resolve the missing-table tenant's pool at all. This is the regression
	// guard: if any of the four reads (the resets in particular) bypassed the
	// presence guard, it would call PoolForTenant again here and this assertion
	// would fail.
	secondEvent := createEventForTenant(t, repo, poolTenantA, "evt.a")
	secondDispatcher := newPoolDispatcher(t, repo, handlers)
	runDispatcherUntil(t, secondDispatcher, func() bool {
		status, ok := statusInDB(t, h.pools["tenant_a"], secondEvent.ID)

		return ok && status == outbox.OutboxStatusPublished
	})
	require.EqualValues(t, 2, handledA.Load())

	callsAfterCycle2 := resolver.poolForTenantCalls(poolTenantD)
	require.Equal(t, callsAfterCycle1, callsAfterCycle2,
		"presence cached within TTL: a second full dispatch cycle (resets + listing) "+
			"must not re-resolve the missing-table tenant's pool")
}

// ---- Test 5: full lifecycle per tenant ----

func TestIntegration_Pool_FullLifecyclePerTenant(t *testing.T) {
	h := newPoolHarness(t,
		[]string{"root_db", "tenant_a", "tenant_b"},
		map[string]bool{"root_db": true, "tenant_a": true, "tenant_b": true},
	)

	resolver := newMapPoolResolver(
		map[string]*sql.DB{
			poolTenantA: h.pools["tenant_a"],
			poolTenantB: h.pools["tenant_b"],
		},
		[]string{poolTenantA, poolTenantB},
	)

	repo := newPoolRepo(t, h, resolver)

	evA := createEventForTenant(t, repo, poolTenantA, "evt.life")
	evB := createEventForTenant(t, repo, poolTenantB, "evt.life")

	// Pre-dispatch: both PENDING in their own DB.
	for _, tc := range []struct {
		pool *sql.DB
		id   uuid.UUID
	}{{h.pools["tenant_a"], evA.ID}, {h.pools["tenant_b"], evB.ID}} {
		status, ok := statusInDB(t, tc.pool, tc.id)
		require.True(t, ok)
		require.Equal(t, outbox.OutboxStatusPending, status)
	}

	handlers := outbox.NewHandlerRegistry()
	require.NoError(t, handlers.Register("evt.life", func(_ context.Context, _ *outbox.OutboxEvent) error {
		return nil
	}))

	dispatcher := newPoolDispatcher(t, repo, handlers)
	runDispatcherUntil(t, dispatcher, func() bool {
		statusA, okA := statusInDB(t, h.pools["tenant_a"], evA.ID)
		statusB, okB := statusInDB(t, h.pools["tenant_b"], evB.ID)

		return okA && okB && statusA == outbox.OutboxStatusPublished && statusB == outbox.OutboxStatusPublished
	})

	// Post-dispatch: PENDING -> PUBLISHED, verified by querying each DB directly.
	statusA, _ := statusInDB(t, h.pools["tenant_a"], evA.ID)
	require.Equal(t, outbox.OutboxStatusPublished, statusA)
	statusB, _ := statusInDB(t, h.pools["tenant_b"], evB.ID)
	require.Equal(t, outbox.OutboxStatusPublished, statusB)
}

func TestIntegration_ModulePoolResolver_DispatchesGenericAndModuleRowsExactlyOnce(t *testing.T) {
	harness := newPoolHarness(t,
		[]string{"root_db", "tenant_generic", "tenant_consignado"},
		map[string]bool{"root_db": true, "tenant_generic": true, "tenant_consignado": true},
	)

	generic := newMapPoolResolver(
		map[string]*sql.DB{poolTenantA: harness.pools["tenant_generic"]},
		[]string{poolTenantA},
	)
	module := newMapPoolResolver(
		map[string]*sql.DB{poolTenantA: harness.pools["tenant_consignado"]},
		nil,
	)
	resolver, err := NewModulePoolResolver(
		generic,
		poolDefaultTenant,
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			return integrationTenantConfigWithDatabases("tenant_generic", "tenant_consignado"), nil
		},
		ModulePool{Name: "consignado", Resolver: module},
	)
	require.NoError(t, err)

	repo := newPoolRepo(t, harness, resolver)
	genericEvent := createEventForTenant(t, repo, poolTenantA, "evt.module-aware")
	moduleEvent := createEventInPool(t, harness.pools["tenant_consignado"], poolTenantA, "evt.module-aware")

	var seenMu sync.Mutex
	seenByEvent := make(map[uuid.UUID][]string)
	handlers := outbox.NewHandlerRegistry()
	require.NoError(t, handlers.Register("evt.module-aware", func(ctx context.Context, event *outbox.OutboxEvent) error {
		tenantID, ok := outbox.TenantIDFromContext(ctx)
		require.True(t, ok)

		seenMu.Lock()
		seenByEvent[event.ID] = append(seenByEvent[event.ID], tenantID)
		seenMu.Unlock()

		return nil
	}))

	dispatcher := newPoolDispatcher(t, repo, handlers)
	runDispatcherUntil(t, dispatcher, func() bool {
		genericStatus, genericOK := statusInDB(t, harness.pools["tenant_generic"], genericEvent.ID)
		moduleStatus, moduleOK := statusInDB(t, harness.pools["tenant_consignado"], moduleEvent.ID)

		return genericOK && moduleOK && genericStatus == outbox.OutboxStatusPublished && moduleStatus == outbox.OutboxStatusPublished
	})

	seenMu.Lock()
	require.Equal(t, []string{poolTenantA}, seenByEvent[genericEvent.ID])
	require.Equal(t, []string{poolTenantA}, seenByEvent[moduleEvent.ID])
	seenMu.Unlock()
	require.Equal(t, 1, countInDB(t, harness.pools["tenant_generic"]))
	require.Equal(t, 1, countInDB(t, harness.pools["tenant_consignado"]))
}

func TestIntegration_ModulePoolResolver_SharedPhysicalDatabaseScansOnce(t *testing.T) {
	harness := newPoolHarness(t,
		[]string{"root_db", "tenant_shared"},
		map[string]bool{"root_db": true, "tenant_shared": true},
	)

	generic := newMapPoolResolver(
		map[string]*sql.DB{poolTenantA: harness.pools["tenant_shared"]},
		[]string{poolTenantA},
	)
	module := newMapPoolResolver(
		map[string]*sql.DB{poolTenantA: harness.pools["tenant_shared"]},
		nil,
	)
	resolver, err := NewModulePoolResolver(
		generic,
		poolDefaultTenant,
		func(context.Context, string) (*tmcore.TenantConfig, error) {
			return integrationTenantConfigWithDatabases("tenant_shared", "tenant_shared"), nil
		},
		ModulePool{Name: "consignado", Resolver: module},
	)
	require.NoError(t, err)

	scopes, err := resolver.ListTenantDispatchScopes(context.Background())
	require.NoError(t, err)
	require.Equal(t, []outbox.TenantDispatchScope{{TenantID: poolTenantA}}, scopes)

	repo := newPoolRepo(t, harness, resolver)
	event := createEventForTenant(t, repo, poolTenantA, "evt.shared")

	var handled atomic.Int32
	handlers := outbox.NewHandlerRegistry()
	require.NoError(t, handlers.Register("evt.shared", func(ctx context.Context, handledEvent *outbox.OutboxEvent) error {
		tenantID, ok := outbox.TenantIDFromContext(ctx)
		require.True(t, ok)
		require.Equal(t, poolTenantA, tenantID)
		require.Equal(t, event.ID, handledEvent.ID)
		handled.Add(1)

		return nil
	}))

	dispatcher := newPoolDispatcher(t, repo, handlers)
	runDispatcherUntil(t, dispatcher, func() bool {
		status, ok := statusInDB(t, harness.pools["tenant_shared"], event.ID)

		return ok && status == outbox.OutboxStatusPublished
	})

	require.EqualValues(t, 1, handled.Load())
	require.Equal(t, 0, module.poolForTenantCalls(poolTenantA), "deduplicated module scope must not scan the shared pool again")
}

// ---- Test 6: fail-closed on unknown tenant ----

func TestIntegration_Pool_FailClosedUnknownTenant(t *testing.T) {
	h := newPoolHarness(t,
		[]string{"root_db", "tenant_a"},
		map[string]bool{"root_db": true, "tenant_a": true},
	)

	resolver := newMapPoolResolver(
		map[string]*sql.DB{
			poolTenantA: h.pools["tenant_a"],
		},
		[]string{poolTenantA},
	)

	repo := newPoolRepo(t, h, resolver)

	// A tenant ID absent from the resolver map: write path must fail closed.
	unknownCtx := outbox.ContextWithTenantID(context.Background(), poolTenantB)
	event, err := outbox.NewOutboxEvent(unknownCtx, "evt.unknown", uuid.New(), []byte(`{"ok":true}`))
	require.NoError(t, err)

	_, err = repo.Create(unknownCtx, event)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrTenantPoolUnavailable)

	// Read path must also fail closed.
	_, err = repo.ListPending(unknownCtx, 10)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrTenantPoolUnavailable)

	// No rows touched in any database.
	require.Equal(t, 0, countInDB(t, h.pools["tenant_a"]))
	require.Equal(t, 0, countInDB(t, h.pools["root_db"]))
}
