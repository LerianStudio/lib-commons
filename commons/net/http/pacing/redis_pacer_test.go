//go:build unit

package pacing_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v6/commons"
	"github.com/LerianStudio/lib-commons/v6/commons/net/http/pacing"
	libRedis "github.com/LerianStudio/lib-commons/v6/commons/redis"
	libLog "github.com/LerianStudio/lib-observability/v2/log"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testPrefix   = "dataprev"
	shortDeadine = 60 * time.Millisecond
	fastPoll     = 5 * time.Millisecond
)

// TestMain allows the plaintext miniredis endpoint for the whole binary so
// individual tests can call t.Parallel() without racing on the environment.
func TestMain(m *testing.M) {
	if err := os.Setenv(commons.EnvAllowInsecureTLS, "true"); err != nil {
		fmt.Fprintln(os.Stderr, "pacing tests: cannot set ALLOW_INSECURE_TLS:", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func newRedisClient(t *testing.T, mr *miniredis.Miniredis) *libRedis.Client {
	t.Helper()

	conn, err := libRedis.New(t.Context(), libRedis.Config{
		Topology: libRedis.Topology{
			Standalone: &libRedis.StandaloneTopology{Address: mr.Addr()},
		},
		Logger: &libLog.NopLogger{},
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Logf("redis close: %v", closeErr)
		}
	})

	return conn
}

// boundedCtx bounds every acquire in the unit suite. A fail-closed guard that
// regresses into "refused, keep retrying" would otherwise hang the whole suite
// instead of failing; with a bound it fails on the wrong error, immediately.
func boundedCtx(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)

	return ctx
}

func newPacer(t *testing.T, mr *miniredis.Miniredis, opts ...pacing.Option) *pacing.Pacer {
	t.Helper()

	p, err := pacing.NewPacer(newRedisClient(t, mr), testPrefix, opts...)
	require.NoError(t, err)

	return p
}

func constRate(r float64) pacing.RateProvider {
	return func(context.Context) (float64, error) { return r, nil }
}

func mustTenantBucket(t *testing.T, id string, r pacing.RateProvider) pacing.Bucket {
	t.Helper()

	b, err := pacing.TenantBucket(id, r)
	require.NoError(t, err)

	return b
}

func mustInstitutionBucket(t *testing.T, id string, r pacing.RateProvider) pacing.Bucket {
	t.Helper()

	b, err := pacing.InstitutionBucket(id, r)
	require.NoError(t, err)

	return b
}

// bucketKeys returns every pacing key except the clock watermark, which is not
// a permit and is deliberately written on refused evaluations too.
func bucketKeys(t *testing.T, mr *miniredis.Miniredis) []string {
	t.Helper()

	out := make([]string, 0, 4)

	for _, k := range mr.Keys() {
		if !strings.HasSuffix(k, ":clock") {
			out = append(out, k)
		}
	}

	return out
}

func keyWithSegment(t *testing.T, mr *miniredis.Miniredis, segment string) string {
	t.Helper()

	for _, k := range bucketKeys(t, mr) {
		if strings.Contains(k, ":"+segment+":") {
			return k
		}
	}

	t.Fatalf("no pacing key with segment %q in %v", segment, mr.Keys())

	return ""
}

func readKey(t *testing.T, mr *miniredis.Miniredis, key string) string {
	t.Helper()

	v, err := mr.Get(key)
	require.NoError(t, err)

	return v
}

// ---------------------------------------------------------------------------
// Bucket identity
// ---------------------------------------------------------------------------

func TestTenantBucket_AcceptsEveryIdentityCanonicalizationAccepts(t *testing.T) {
	t.Parallel()

	accepted := []struct {
		name string
		id   string
	}{
		{"dashed uuid", "1f2e3d4c-5b6a-7980-9182-a3b4c5d6e7f8"},
		{"dashless uuid", "1f2e3d4c5b6a79809182a3b4c5d6e7f8"},
		{"uppercase uuid", "1F2E3D4C-5B6A-7980-9182-A3B4C5D6E7F8"},
		{"slug", "tenant-123-abc"},
		{"default", "default"},
		{"underscored", "tenant_9"},
	}

	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := pacing.TenantBucket(tc.id, constRate(1))
			require.NoError(t, err)
		})
	}
}

func TestTenantBucket_RejectsWithoutLeakingTheIdentity(t *testing.T) {
	t.Parallel()

	rejected := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"leading dash", "-tenant"},
		{"illegal byte", "tenant!9"},
		{"urn uuid", "urn:uuid:1f2e3d4c-5b6a-7980-9182-a3b4c5d6e7f8"},
		{"braced uuid", "{1f2e3d4c-5b6a-7980-9182-a3b4c5d6e7f8}"},
		{"colon", "tenant:9"},
		{"too long", strings.Repeat("a", 4096)},
	}

	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := pacing.TenantBucket(tc.id, constRate(1))
			require.ErrorIs(t, err, pacing.ErrInvalidIdentity)

			if tc.id != "" {
				assert.NotContains(t, err.Error(), tc.id, "error message must not embed the rejected identity")
			}
		})
	}
}

func TestTenantBucket_RejectsNilRateProvider(t *testing.T) {
	t.Parallel()

	_, err := pacing.TenantBucket("default", nil)
	require.ErrorIs(t, err, pacing.ErrInvalidRate)
}

func TestInstitutionBucket_ValidatesSeparately(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"077", "itau-unibanco", "BTG_1"} {
		_, err := pacing.InstitutionBucket(id, constRate(1))
		require.NoErrorf(t, err, "institution %q must be accepted", id)
	}

	for _, id := range []string{"", "0 77", "-077", "banco:1"} {
		_, err := pacing.InstitutionBucket(id, constRate(1))
		require.ErrorIsf(t, err, pacing.ErrInvalidIdentity, "institution %q must be rejected", id)
	}
}

func TestPacer_Acquire_UUIDSpellingsShareOneBucket(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr)

	dashed := mustTenantBucket(t, "1f2e3d4c-5b6a-7980-9182-a3b4c5d6e7f8", constRate(1000))
	require.NoError(t, p.Acquire(boundedCtx(t), dashed))

	dashedKeys := bucketKeys(t, mr)
	require.Len(t, dashedKeys, 1)

	mr.FlushAll()

	dashless := mustTenantBucket(t, "1F2E3D4C5B6A79809182A3B4C5D6E7F8", constRate(1000))
	require.NoError(t, p.Acquire(boundedCtx(t), dashless))

	assert.Equal(t, dashedKeys, bucketKeys(t, mr),
		"dashed and dashless spellings of one tenant must collapse onto one bucket")
}

func TestPacer_Acquire_KeysAreDigestedAndSameSlot(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr)

	tenantID := "1f2e3d4c-5b6a-7980-9182-a3b4c5d6e7f8"
	tenant := mustTenantBucket(t, tenantID, constRate(1000))
	inst := mustInstitutionBucket(t, "077", constRate(1000))

	require.NoError(t, p.Acquire(boundedCtx(t), tenant, inst))

	keys := mr.Keys()
	require.Len(t, keys, 3, "two buckets plus the clock watermark")

	for _, k := range keys {
		assert.NotContains(t, k, tenantID, "raw tenant identity must not appear in a Redis key")
		assert.Contains(t, k, "{"+testPrefix+"}",
			"every key needs the prefix hash tag so one EVAL stays inside one cluster slot")
	}
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNewPacer_Validation(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	conn := newRedisClient(t, mr)

	_, err := pacing.NewPacer(nil, testPrefix)
	require.ErrorIs(t, err, pacing.ErrPacerUnavailable)

	for _, bad := range []string{"", "has space", "-lead", "with:colon", "with{brace"} {
		_, prefixErr := pacing.NewPacer(conn, bad)
		require.ErrorIsf(t, prefixErr, pacing.ErrInvalidPrefix, "prefix %q must be rejected", bad)
	}

	_, err = pacing.NewPacer(conn, testPrefix, pacing.WithMaxRate(0))
	require.ErrorIs(t, err, pacing.ErrInvalidRate)

	_, err = pacing.NewPacer(conn, testPrefix, pacing.WithMaxRate(1e-9))
	require.ErrorIs(t, err, pacing.ErrInvalidRate, "a maximum rate below one call per day must be rejected at construction")

	_, err = pacing.NewPacer(conn, testPrefix, pacing.WithPollInterval(0))
	require.ErrorIs(t, err, pacing.ErrInvalidPollInterval)

	p, err := pacing.NewPacer(conn, testPrefix, pacing.WithLogger(&libLog.NopLogger{}), nil)
	require.NoError(t, err)
	require.NotNil(t, p, "a nil option must be skipped, not dereferenced")
}

// recordingLogger captures warn-level records so a test can assert that a
// fail-closed refusal was actually reported, not just returned.
type recordingLogger struct {
	libLog.NopLogger

	mu   sync.Mutex
	warn []string
}

func (r *recordingLogger) Log(_ context.Context, level libLog.Level, msg string, _ ...libLog.Field) {
	if level != libLog.LevelWarn {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.warn = append(r.warn, msg)
}

func (r *recordingLogger) warnings() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.warn...)
}

func TestPacer_Acquire_BackendFailureIsLogged(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	rec := &recordingLogger{}
	p := newPacer(t, mr, pacing.WithLogger(rec))

	mr.SetError("LOADING Redis is loading the dataset in memory")

	err := p.Acquire(boundedCtx(t), mustTenantBucket(t, "default", constRate(1000)))
	require.ErrorIs(t, err, pacing.ErrBackendUnavailable)
	assert.NotEmpty(t, rec.warnings(), "a refused outbound call must be logged")
}

func TestPacer_Acquire_RejectsEmptyAndDuplicateAndZeroBuckets(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr)

	require.ErrorIs(t, p.Acquire(boundedCtx(t)), pacing.ErrNoBuckets)

	tenant := mustTenantBucket(t, "default", constRate(1000))
	require.ErrorIs(t, p.Acquire(boundedCtx(t), tenant, tenant), pacing.ErrDuplicateBucket)

	require.ErrorIs(t, p.Acquire(boundedCtx(t), pacing.Bucket{}), pacing.ErrInvalidIdentity)

	assert.Empty(t, mr.Keys(), "a rejected acquire must not reach Redis")
}

func TestPacer_NilReceiverFailsClosed(t *testing.T) {
	t.Parallel()

	var p *pacing.Pacer

	require.ErrorIs(t, p.Acquire(boundedCtx(t), pacing.Bucket{}), pacing.ErrPacerUnavailable)
}

// ---------------------------------------------------------------------------
// Granting and pacing
// ---------------------------------------------------------------------------

func TestPacer_Acquire_GrantsTheFirstCallImmediately(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr)

	start := time.Now()
	require.NoError(t, p.Acquire(boundedCtx(t), mustTenantBucket(t, "default", constRate(1000))))

	assert.Less(t, time.Since(start), time.Second, "an empty bucket must not delay the first call")
	assert.Len(t, bucketKeys(t, mr), 1)
}

func TestPacer_Acquire_StoresTheGrantInstantExactly(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	frozen := time.Unix(1_800_000_000, 123_456_000).UTC()
	mr.SetTime(frozen)

	p := newPacer(t, mr)

	require.NoError(t, p.Acquire(boundedCtx(t), mustTenantBucket(t, "default", constRate(100))))

	stored := readKey(t, mr, keyWithSegment(t, mr, "tenant"))

	// The bucket holds the LAST GRANT, not the next admission: the next admission
	// is derived from the interval in force at evaluation time, which is what lets
	// a rate raised at runtime take effect immediately.
	assert.Equal(t, strconv.FormatInt(frozen.UnixMicro(), 10), stored,
		"the bucket must hold the grant instant as a plain microsecond integer")
	assert.NotContains(t, stored, "e", "an exponent spelling is not an inspectable timestamp")
}

func TestPacer_Acquire_HotRateIncreaseShortensAnInFlightWait(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr, pacing.WithPollInterval(fastPoll))

	var calls atomic.Int64

	// One call per hour, until the third read of the provider raises it. The
	// second Acquire therefore starts a wait of roughly an hour and must still
	// return as soon as the raised rate is observed.
	rate := func(context.Context) (float64, error) {
		if calls.Add(1) < 3 {
			return 1.0 / 3600.0, nil
		}

		return 1000, nil
	}

	tenant := mustTenantBucket(t, "default", rate)
	require.NoError(t, p.Acquire(boundedCtx(t), tenant))

	start := time.Now()
	require.NoError(t, p.Acquire(boundedCtx(t), tenant))

	assert.Less(t, time.Since(start), 2*time.Second,
		"a rate raised during a wait must be honoured instead of sleeping out the slow interval")
	assert.GreaterOrEqual(t, calls.Load(), int64(3))
}

func TestPacer_Acquire_PacesTheSecondCall(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr)
	tenant := mustTenantBucket(t, "default", constRate(50)) // one call every 20ms

	require.NoError(t, p.Acquire(boundedCtx(t), tenant))

	start := time.Now()
	require.NoError(t, p.Acquire(boundedCtx(t), tenant))

	assert.GreaterOrEqual(t, time.Since(start), 15*time.Millisecond,
		"50/s must hold the second call for roughly one emission interval")
}

func TestPacer_Acquire_RateLoweredAtRuntimeStillHoldsTheOldGrant(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	frozen := time.Unix(1_800_000_000, 0).UTC()
	mr.SetTime(frozen)

	p := newPacer(t, mr, pacing.WithPollInterval(fastPoll))

	var slow atomic.Bool

	// 1000/s until the rate is lowered to one call per ten seconds.
	rate := func(context.Context) (float64, error) {
		if slow.Load() {
			return 0.1, nil
		}

		return 1000, nil
	}

	tenant := mustTenantBucket(t, "default", rate)
	require.NoError(t, p.Acquire(boundedCtx(t), tenant))

	// Two seconds later: far past any lifetime derived from the fast interval, and
	// far short of the ten seconds the lowered rate now demands.
	mr.SetTime(frozen.Add(2 * time.Second))
	mr.FastForward(2 * time.Second)
	slow.Store(true)

	ctx, cancel := context.WithTimeout(t.Context(), shortDeadine)
	defer cancel()

	require.ErrorIs(t, p.Acquire(ctx, tenant), pacing.ErrWaitAborted,
		"lowering the rate must not expire the state that enforces it")
}

// ---------------------------------------------------------------------------
// Zero rate: both exits
// ---------------------------------------------------------------------------

func TestPacer_Acquire_ZeroRateBlocksUntilContextIsCancelled(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr, pacing.WithPollInterval(fastPoll))

	ctx, cancel := context.WithTimeout(t.Context(), shortDeadine)
	defer cancel()

	err := p.Acquire(ctx, mustTenantBucket(t, "default", constRate(0)))
	require.ErrorIs(t, err, pacing.ErrWaitAborted)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	assert.Empty(t, mr.Keys(), "a zero rate must never reserve anything")
}

func TestPacer_Acquire_ZeroRateUnblocksWhenTheProviderTurnsPositive(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr, pacing.WithPollInterval(fastPoll))

	var calls atomic.Int64

	rate := func(context.Context) (float64, error) {
		if calls.Add(1) < 3 {
			return 0, nil
		}

		return 1000, nil
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	require.NoError(t, p.Acquire(ctx, mustTenantBucket(t, "default", rate)))
	assert.GreaterOrEqual(t, calls.Load(), int64(3), "the provider must be re-read on every wait")
	assert.Len(t, bucketKeys(t, mr), 1)
}

// ---------------------------------------------------------------------------
// Rate validation, fail closed
// ---------------------------------------------------------------------------

func TestPacer_Acquire_RejectsRatesOutsideTheCallerMaximum(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr, pacing.WithMaxRate(10))

	err := p.Acquire(boundedCtx(t), mustTenantBucket(t, "default", constRate(10.5)))
	require.ErrorIs(t, err, pacing.ErrInvalidRate)
	assert.Empty(t, mr.Keys(), "a rejected rate must not reach Redis")
}

func TestPacer_Acquire_RejectsUnusableRateValues(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr)

	for name, r := range map[string]float64{
		"nan":               math.NaN(),
		"positive infinity": math.Inf(1),
		"negative":          -1,
		"absurdly slow":     1e-9,
	} {
		t.Run(name, func(t *testing.T) {
			err := p.Acquire(boundedCtx(t), mustTenantBucket(t, "default", constRate(r)))
			require.ErrorIs(t, err, pacing.ErrInvalidRate)
		})
	}
}

func TestPacer_Acquire_RateProviderFailureFailsClosed(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr)
	boom := errors.New("systemplane unreachable")

	rate := func(context.Context) (float64, error) { return 0, boom }

	err := p.Acquire(boundedCtx(t), mustTenantBucket(t, "default", rate))
	require.ErrorIs(t, err, pacing.ErrRateUnavailable)
	require.ErrorIs(t, err, boom)
	assert.Empty(t, mr.Keys(), "an unknown rate must not reach Redis")
}

// ---------------------------------------------------------------------------
// Atomicity across buckets
// ---------------------------------------------------------------------------

func TestPacer_Acquire_ZeroRateOnSecondBucketChargesNothingToTheFirst(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr, pacing.WithPollInterval(fastPoll))

	tenant := mustTenantBucket(t, "default", constRate(1000))
	inst := mustInstitutionBucket(t, "077", constRate(0))

	ctx, cancel := context.WithTimeout(t.Context(), shortDeadine)
	defer cancel()

	require.ErrorIs(t, p.Acquire(ctx, tenant, inst), pacing.ErrWaitAborted)
	assert.Empty(t, mr.Keys(),
		"a paused institution must not burn the tenant permit")
}

func TestPacer_Acquire_RefusedMultiBucketEvaluationMutatesNoBucket(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	frozen := time.Unix(1_800_000_000, 0).UTC()
	mr.SetTime(frozen)

	p := newPacer(t, mr, pacing.WithPollInterval(fastPoll))
	tenant := mustTenantBucket(t, "default", constRate(100)) // 10ms interval
	inst := mustInstitutionBucket(t, "077", constRate(100))

	require.NoError(t, p.Acquire(boundedCtx(t), tenant, inst))

	tenantKey := keyWithSegment(t, mr, "tenant")
	instKey := keyWithSegment(t, mr, "inst")
	tenantBefore := readKey(t, mr, tenantKey)
	instBefore := readKey(t, mr, instKey)

	// Advance the clock by less than one emission interval: every retry is still
	// refused, but a write on the refusal path would now store a DIFFERENT
	// arrival time, so the assertions below can see it.
	mr.SetTime(frozen.Add(time.Millisecond))

	ctx, cancel := context.WithTimeout(t.Context(), shortDeadine)
	defer cancel()

	require.ErrorIs(t, p.Acquire(ctx, tenant, inst), pacing.ErrWaitAborted)

	assert.Equal(t, tenantBefore, readKey(t, mr, tenantKey))
	assert.Equal(t, instBefore, readKey(t, mr, instKey))
}

func TestPacer_Acquire_BlockedSecondBucketDoesNotChargeTheReadyFirstBucket(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	frozen := time.Unix(1_800_000_000, 0).UTC()
	mr.SetTime(frozen)

	p := newPacer(t, mr, pacing.WithPollInterval(fastPoll))
	tenant := mustTenantBucket(t, "default", constRate(100))
	inst := mustInstitutionBucket(t, "077", constRate(100))

	require.NoError(t, p.Acquire(boundedCtx(t), tenant, inst))

	tenantKey := keyWithSegment(t, mr, "tenant")
	instKey := keyWithSegment(t, mr, "inst")

	// Move the clock forward so the tenant bucket is ready again, then push the
	// institution bucket ten seconds into the future.
	later := frozen.Add(time.Second)
	mr.SetTime(later)

	blockedUntil := later.Add(10 * time.Second).UnixMicro()
	require.NoError(t, mr.Set(instKey, strconv.FormatInt(blockedUntil, 10)))

	tenantBefore := readKey(t, mr, tenantKey)

	ctx, cancel := context.WithTimeout(t.Context(), shortDeadine)
	defer cancel()

	require.ErrorIs(t, p.Acquire(ctx, tenant, inst), pacing.ErrWaitAborted)

	assert.Equal(t, tenantBefore, readKey(t, mr, tenantKey),
		"the ready tenant bucket must not be charged while the institution bucket blocks")
	assert.Equal(t, strconv.FormatInt(blockedUntil, 10), readKey(t, mr, instKey))
}

// ---------------------------------------------------------------------------
// Backend failures, fail closed
// ---------------------------------------------------------------------------

func TestPacer_Acquire_RedisDownFailsClosed(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr)

	mr.Close()

	err := p.Acquire(boundedCtx(t), mustTenantBucket(t, "default", constRate(1000)))
	require.ErrorIs(t, err, pacing.ErrBackendUnavailable)
}

func TestPacer_Acquire_RedisErrorReplyFailsClosed(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr)

	mr.SetError("LOADING Redis is loading the dataset in memory")

	err := p.Acquire(boundedCtx(t), mustTenantBucket(t, "default", constRate(1000)))
	require.ErrorIs(t, err, pacing.ErrBackendUnavailable)
}

func TestPacer_Acquire_MalformedBucketStateFailsClosed(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr)
	tenant := mustTenantBucket(t, "default", constRate(1000))

	require.NoError(t, p.Acquire(boundedCtx(t), tenant))

	tenantKey := keyWithSegment(t, mr, "tenant")
	require.NoError(t, mr.Set(tenantKey, "not-a-timestamp"))

	err := p.Acquire(boundedCtx(t), tenant)
	require.ErrorIs(t, err, pacing.ErrBackendUnavailable)
}

func TestPacer_Acquire_ClockGoingBackwardsFailsClosed(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	frozen := time.Unix(1_800_000_000, 0).UTC()
	mr.SetTime(frozen)

	p := newPacer(t, mr)
	tenant := mustTenantBucket(t, "default", constRate(1000))

	require.NoError(t, p.Acquire(boundedCtx(t), tenant))

	// A Redis failover onto a node whose clock lags.
	mr.SetTime(frozen.Add(-time.Second))

	err := p.Acquire(boundedCtx(t), tenant)
	require.ErrorIs(t, err, pacing.ErrClockWentBackwards)
}

func TestPacer_Acquire_MalformedClockWatermarkFailsClosed(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr)
	tenant := mustTenantBucket(t, "default", constRate(1000))

	require.NoError(t, p.Acquire(boundedCtx(t), tenant))

	var clockKey string

	for _, k := range mr.Keys() {
		if strings.HasSuffix(k, ":clock") {
			clockKey = k
		}
	}

	require.NotEmpty(t, clockKey)
	require.NoError(t, mr.Set(clockKey, "tuesday"))

	err := p.Acquire(boundedCtx(t), tenant)
	require.ErrorIs(t, err, pacing.ErrBackendUnavailable)
}

func TestPacer_Acquire_AlreadyCancelledContextNeverReachesRedis(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	p := newPacer(t, mr)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := p.Acquire(ctx, mustTenantBucket(t, "default", constRate(1000)))
	require.ErrorIs(t, err, pacing.ErrWaitAborted)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, mr.Keys())
}
