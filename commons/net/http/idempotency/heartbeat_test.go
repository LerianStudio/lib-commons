//go:build unit

package idempotency

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// extendingStore adds the optional LeaseExtender capability to expiringStore,
// on the same real clock, so a lapse the heartbeat fails to prevent shows up as
// a fresh acquisition exactly as it does in production. failFirst makes the
// first N Extend calls error, to prove one failure does not end the heartbeat.
type extendingStore struct {
	*expiringStore
	extends   atomic.Int64
	failFirst int64
}

func (s *extendingStore) Extend(_ context.Context, key string, expected []byte, ttl time.Duration) (bool, error) {
	if s.extends.Add(1) <= s.failFirst {
		return false, errors.New("idempotency test store: transient extend failure")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, found := s.live(key)
	if !found || !bytes.Equal(record.value, expected) {
		return false, nil
	}

	record.expiresAt = time.Now().Add(ttl)
	s.records[key] = record

	return true, nil
}

var _ LeaseExtender = (*extendingStore)(nil)

func TestRedisStore_Extend_RenewsOnlyTheCallersLease(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	store := newRedisStore(newRedisClient(t, mr))
	ctx := context.Background()
	key := "idempotency:tenant-a:extend"
	processing := []byte("processing")

	_, acquired, err := store.Acquire(ctx, key, processing, time.Second)
	require.NoError(t, err)
	require.True(t, acquired)

	applied, err := store.Extend(ctx, key, processing, time.Hour)
	require.NoError(t, err)
	assert.True(t, applied, "the owner's bytes extend the lease")
	assert.Equal(t, time.Hour, mr.TTL(key))

	applied, err = store.Extend(ctx, key, []byte("stale"), 2*time.Hour)
	require.NoError(t, err)
	assert.False(t, applied, "foreign bytes must not extend the lease")
	assert.Equal(t, time.Hour, mr.TTL(key), "a refused extend leaves the TTL")

	_, err = store.Extend(ctx, key, processing, 0)
	require.ErrorIs(t, err, errInvalidTTL)

	applied, err = store.Complete(ctx, key, processing, []byte("completed"), time.Minute)
	require.NoError(t, err)
	require.True(t, applied)

	applied, err = store.Extend(ctx, key, processing, time.Hour)
	require.NoError(t, err)
	assert.False(t, applied, "a completed record is no longer the caller's lease")
	assert.Equal(t, time.Minute, mr.TTL(key), "the retention survives a late extend")

	mr.FastForward(2 * time.Minute)

	applied, err = store.Extend(ctx, key, processing, time.Hour)
	require.NoError(t, err)
	assert.False(t, applied, "an absent key cannot be extended")
	assert.False(t, mr.Exists(key), "extend must never recreate a key")
}

// heartbeatApp holds the FIRST request in flight until release closes and
// answers every later request at once, so a duplicate that wrongly acquires the
// key completes instead of deadlocking the test.
func heartbeatApp(mw fiber.Handler, calls *atomic.Int64, entered chan<- struct{}, release <-chan struct{}) *fiber.App {
	app := fiber.New()
	app.Use(tenantMiddleware("tenant-heartbeat"))
	app.Use(mw)
	app.Post("/test", func(c fiber.Ctx) error {
		if calls.Add(1) == 1 {
			entered <- struct{}{}
			<-release
		}

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	return app
}

func TestCheck_ProcessingHeartbeat_HoldsTheKeyPastTheLease(t *testing.T) {
	t.Parallel()

	const (
		lease    = 250 * time.Millisecond
		interval = 25 * time.Millisecond
	)

	tests := []struct {
		name       string
		heartbeat  bool
		wantStatus int
		wantCalls  int64
	}{
		// Today's behaviour, pinned: the lease lapses under a handler still
		// running and the duplicate executes the mutation a second time.
		{name: "heartbeat off lets the duplicate run", heartbeat: false, wantStatus: http.StatusCreated, wantCalls: 2},
		{name: "heartbeat on refuses the duplicate", heartbeat: true, wantStatus: http.StatusConflict, wantCalls: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := &extendingStore{expiringStore: newExpiringStore()}
			opts := []Option{WithKeyTTL(time.Hour), WithProcessingTTL(lease)}

			if tt.heartbeat {
				opts = append(opts, WithProcessingHeartbeat(interval))
			}

			middleware := NewWithStore(store, opts...)
			require.NoError(t, middleware.Err())

			var calls atomic.Int64

			entered := make(chan struct{})
			release := make(chan struct{})
			app := heartbeatApp(middleware.Check(), &calls, entered, release)

			first := postAsync(app, "heartbeat-key")
			awaitEntered(t, entered, first)

			time.Sleep(3 * lease) // the handler outlives its lease several times over

			second := doPost(t, app, "heartbeat-key")
			second.Body.Close()

			assert.Equal(t, tt.wantStatus, second.StatusCode)

			close(release)
			awaitStatus(t, first)

			assert.Equal(t, tt.wantCalls, calls.Load())
		})
	}
}

func TestCheck_ProcessingHeartbeat_SurvivesFailuresAndStopsWithTheHandler(t *testing.T) {
	t.Parallel()

	const (
		lease    = 250 * time.Millisecond
		interval = 25 * time.Millisecond
	)

	// The first extend errors: the loop must keep beating and still hold.
	store := &extendingStore{expiringStore: newExpiringStore(), failFirst: 1}
	middleware := NewWithStore(store,
		WithKeyTTL(time.Hour), WithProcessingTTL(lease), WithProcessingHeartbeat(interval))
	require.NoError(t, middleware.Err())

	var calls atomic.Int64

	entered := make(chan struct{})
	release := make(chan struct{})
	app := heartbeatApp(middleware.Check(), &calls, entered, release)

	first := postAsync(app, "heartbeat-stop-key")
	awaitEntered(t, entered, first)

	time.Sleep(3 * lease)

	second := doPost(t, app, "heartbeat-stop-key")
	second.Body.Close()
	assert.Equal(t, http.StatusConflict, second.StatusCode, "a failed beat must not end the heartbeat")

	close(release)
	assert.Equal(t, http.StatusCreated, awaitStatus(t, first))

	settled := store.extends.Load()
	require.Greater(t, settled, int64(1))

	time.Sleep(5 * interval)
	assert.Equal(t, settled, store.extends.Load(), "no extend may run after the handler returned")
}

func TestNewWithStore_ProcessingHeartbeat_RefusesAStoreThatCannotExtend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		store Store
		opts  []Option
	}{
		{
			name:  "store without LeaseExtender",
			store: newExpiringStore(),
			opts:  []Option{WithProcessingHeartbeat(10 * time.Millisecond)},
		},
		{
			name:  "interval not shorter than the lease",
			store: &extendingStore{expiringStore: newExpiringStore()},
			opts:  []Option{WithProcessingTTL(time.Second), WithProcessingHeartbeat(time.Second)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			middleware := NewWithStore(tt.store, tt.opts...)
			require.ErrorIs(t, middleware.Err(), ErrHeartbeatMisconfigured)

			var calls atomic.Int64

			app := fiber.New()
			app.Use(tenantMiddleware("tenant-misconfigured"))
			app.Use(middleware.Check())
			app.Post("/test", func(c fiber.Ctx) error {
				calls.Add(1)

				return c.SendStatus(fiber.StatusCreated)
			})

			resp := doPost(t, app, "misconfigured-key")
			resp.Body.Close()

			assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode,
				"a misconfigured heartbeat must never run the mutation unprotected")
			assert.Zero(t, calls.Load())
		})
	}
}

func TestNew_ProcessingHeartbeat_RedisStoreExtends(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	middleware := New(newRedisClient(t, mr), WithProcessingHeartbeat(time.Second))
	require.NoError(t, middleware.Err())
	assert.NotNil(t, middleware.extender, "New must bind the Redis store's LeaseExtender")
	assert.NoError(t, (*Middleware)(nil).Err())
}

// A lease resolved per request can be shorter than the configured interval,
// which construction cannot see. The tick must follow the lease actually
// stored, or the lease lapses before the first beat and the duplicate runs.
func TestCheck_ProcessingHeartbeat_TicksWithinAProviderLease(t *testing.T) {
	t.Parallel()

	const (
		lease    = 150 * time.Millisecond
		interval = time.Second
	)

	store := &extendingStore{expiringStore: newExpiringStore()}
	logger := &recordingLogger{}
	middleware := NewWithStore(store,
		WithLogger(logger),
		WithKeyTTL(time.Hour),
		WithProcessingTTLProvider(func(fiber.Ctx) (time.Duration, error) { return lease, nil }),
		WithProcessingHeartbeat(interval))
	require.NoError(t, middleware.Err(), "a provider lease is invisible to construction")

	var calls atomic.Int64

	entered := make(chan struct{})
	release := make(chan struct{})
	app := heartbeatApp(middleware.Check(), &calls, entered, release)

	first := postAsync(app, "provider-lease-key")
	awaitEntered(t, entered, first)

	time.Sleep(4 * lease)

	second := doPost(t, app, "provider-lease-key")
	second.Body.Close()
	assert.Equal(t, http.StatusConflict, second.StatusCode, "the lease must be renewed within its own span")

	close(release)
	assert.Equal(t, http.StatusCreated, awaitStatus(t, first))
	assert.Equal(t, int64(1), calls.Load(), "the mutation must run exactly once")

	line := logger.find(t, obs.LevelWarn, "heartbeat interval shortened")
	assert.Equal(t, interval, line.kv["configured_interval"])
	assert.Equal(t, lease, line.kv["effective_lease"])
	assert.Equal(t, lease/3, line.kv["tick"])
	assert.Equal(t, 1, logger.count("heartbeat interval shortened"), "one warning per request, not per beat")
}

func TestHeartbeatTick(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		interval, lease time.Duration
		want            time.Duration
	}{
		{name: "interval already inside the lease", interval: 10 * time.Second, lease: time.Minute, want: 10 * time.Second},
		{name: "lease shorter than the interval", interval: 15 * time.Second, lease: 10 * time.Second, want: 10 * time.Second / 3},
		{name: "pathological lease floors the tick", interval: time.Second, lease: 30 * time.Millisecond, want: heartbeatMinTick},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, heartbeatTick(tt.interval, tt.lease))
		})
	}
}

// beat runs off the request goroutine, so the tenant it logs must be the one
// the request resolved, including through a tenant provider.
func TestCheck_ProcessingHeartbeat_LogsTheResolvedTenant(t *testing.T) {
	t.Parallel()

	store := &extendingStore{expiringStore: newExpiringStore(), failFirst: 1}
	logger := &recordingLogger{}
	middleware := NewWithStore(store,
		WithLogger(logger),
		WithKeyTTL(time.Hour),
		WithProcessingTTL(250*time.Millisecond),
		WithTenantProvider(func(fiber.Ctx) (string, error) { return "tenant-from-provider", nil }),
		WithProcessingHeartbeat(25*time.Millisecond))
	require.NoError(t, middleware.Err())

	app := fiber.New()
	app.Use(middleware.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		time.Sleep(100 * time.Millisecond)

		return c.SendStatus(fiber.StatusCreated)
	})

	resp := doPost(t, app, "provider-tenant-key")
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	line := logger.find(t, obs.LevelWarn, "processing heartbeat failed")
	assert.Equal(t, "tenant-from-provider", line.kv["tenant_id"])
}

// count reports how many lines, at any level, contain substring.
func (l *recordingLogger) count(substring string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	n := 0

	for _, line := range l.lines {
		if strings.Contains(line.msg, substring) {
			n++
		}
	}

	return n
}
