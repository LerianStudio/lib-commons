//go:build unit

package idempotency

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	chttp "github.com/LerianStudio/lib-commons/v7/commons/constants"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The lease is deliberately far shorter than the retention. Every test here
// then ages the store past the lease and NOT past the retention, which is the
// exact window the defect lived in: the processing record lapses, the key goes
// free, and the same key executes the mutation a second time.
const (
	fenceLease     = 100 * time.Millisecond
	fenceRetention = time.Hour
	pastTheLease   = 5 * time.Minute
)

var errReceiptWrite = errors.New("idempotency test store: receipt write failed")

// transientCompleteFailure fails the first Complete call against a real store
// and delegates every call after it.
//
// The transience is the point, not a convenience. The fence is written with
// Complete too, against the store that just failed, so a store that stayed down
// could not be fenced at all — that is the stated limit of the mechanism. This
// models what was measured instead: one dropped receipt write against a store
// that is healthy again immediately.
type transientCompleteFailure struct {
	Store

	completes atomic.Int64
}

func (s *transientCompleteFailure) Complete(
	ctx context.Context, key string, expected, completed []byte, ttl time.Duration,
) (bool, error) {
	if s.completes.Add(1) == 1 {
		return false, errReceiptWrite
	}

	return s.Store.Complete(ctx, key, expected, completed, ttl)
}

// countingMoneyApp routes POST /test through mw to a handler that counts every
// execution. The count is the finding in every test here: a status can be
// argued about, two executions under one idempotency key cannot.
func countingMoneyApp(mw fiber.Handler, tenantID string, calls *atomic.Int64, handler fiber.Handler) *fiber.App {
	app := fiber.New()
	app.Use(tenantMiddleware(tenantID))
	app.Use(mw)
	app.Post("/test", func(c fiber.Ctx) error {
		calls.Add(1)

		return handler(c)
	})

	return app
}

// realRedisStore builds the shipped Redis store over miniredis, so the Lua
// compare-and-set the fence depends on actually runs.
func realRedisStore(t *testing.T) (Store, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)

	return newRedisStore(newRedisClient(t, mr)), mr
}

// TestCheck_CompletionFailure_FencesTheKeyForTheRetentionWindow walks the
// measured three-step money sequence: a full-settlement payoff commits, its
// receipt write fails, and the client is told the mutation happened.
//
// Step 3 is the one that used to lose money. The processing record lapses with
// the in-flight lease and the key becomes free, so a resend an hour later runs
// the payoff a SECOND time under the same idempotency key — two quotes, two
// collection instruments, two pix demands. Between step 2 and step 3 the only
// thing standing in the way was the prose in the refusal body.
func TestCheck_CompletionFailure_FencesTheKeyForTheRetentionWindow(t *testing.T) {
	t.Parallel()

	store, mr := realRedisStore(t)
	faulty := &transientCompleteFailure{Store: store}

	middleware := NewWithStore(faulty,
		WithKeyTTL(fenceRetention),
		WithProcessingTTL(fenceLease),
	)

	var calls atomic.Int64

	app := countingMoneyApp(middleware.Check(), "tenant-payoff", &calls, func(c fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "payoff armed"})
	})

	// 1. The handler commits and its receipt write fails.
	first := doPost(t, app, "payoff-key")
	firstBody := readBody(t, first)

	require.Equal(t, http.StatusServiceUnavailable, first.StatusCode,
		"the mutation committed and its receipt did not: the caller must be told to reconcile")
	assert.Contains(t, firstBody, "reconcile the original request first")
	assert.Equal(t, int64(1), calls.Load())

	// 2. An immediate resend is refused.
	second := doPost(t, app, "payoff-key")
	secondBody := readBody(t, second)

	assert.Equal(t, http.StatusUnprocessableEntity, second.StatusCode,
		"the key is terminal, so the refusal must be terminal too — not the 409 that invites a retry")
	assert.Contains(t, secondBody, "IDEMPOTENCY_OUTCOME_UNKNOWN")
	assert.Empty(t, second.Header.Get(fiber.HeaderRetryAfter),
		"no Retry-After: waiting inside the retention window never changes this answer")
	assert.NotContains(t, secondBody, "payoff armed",
		"no response was ever stored for this key; answering with one would report an outcome nobody recorded")
	assert.Equal(t, int64(1), calls.Load())

	// 3. A resend after the in-flight lease has lapsed. THIS is the regression.
	mr.FastForward(pastTheLease)

	third := doPost(t, app, "payoff-key")

	assert.Equal(t, http.StatusUnprocessableEntity, third.StatusCode,
		"the fence holds for the retention window, not for the lease that used to lapse")
	assert.Contains(t, readBody(t, third), "IDEMPOTENCY_OUTCOME_UNKNOWN")
	assert.Equal(t, int64(1), calls.Load(),
		"one idempotency key must never arm the payoff twice")
}

// TestCheck_CompletionFailure_FenceIsWrittenForRetentionNotForTheLease reads the
// stored record directly, because the status alone cannot tell a fence that
// holds for an hour from one that holds for the 100ms lease. Asserting the
// state and its expiry pins WHICH lifetime the fence borrowed.
func TestCheck_CompletionFailure_FenceIsWrittenForRetentionNotForTheLease(t *testing.T) {
	t.Parallel()

	store, mr := realRedisStore(t)
	faulty := &transientCompleteFailure{Store: store}

	middleware := NewWithStore(faulty,
		WithKeyTTL(fenceRetention),
		WithProcessingTTL(fenceLease),
	)

	var calls atomic.Int64

	app := countingMoneyApp(middleware.Check(), "tenant-ttl", &calls, func(c fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	doPost(t, app, "ttl-key").Body.Close()

	const key = "idempotency:tenant-ttl:ttl-key"

	stored, err := mr.Get(key)
	require.NoError(t, err, "the key must still hold a record, not have lapsed")
	assert.Contains(t, stored, `"state":"`+keyStateOutcomeUnknown+`"`)
	assert.NotContains(t, stored, `"response"`,
		"the fence stores no body: capture or persistence is exactly what failed")

	assert.Greater(t, mr.TTL(key), fenceLease,
		"a fence sized to the lease would lapse into the re-execution it exists to prevent")
}

// TestCheck_ServerErrorPolicy_DecidesWhetherA5xxFreesTheKey covers the second
// path: the handler answered 5xx and the middleware released the key before the
// application's error handler could say what that 5xx meant.
//
// The two rows are the whole argument. Under the shipped default a 5xx means
// "nothing ran, retry" and the key is freed — correct for a route whose
// failures are refusals, and the behaviour every existing caller depends on.
// Under the fence it means "this may have moved money", and the key is held.
// The middleware cannot tell them apart, so the route declares it.
func TestCheck_ServerErrorPolicy_DecidesWhetherA5xxFreesTheKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts []Option
		// resendStatus is what an immediate resend under the same key receives.
		resendStatus int
		// callsAfterLease is the finding: how many times one idempotency key
		// executed the mutation once the in-flight lease has lapsed.
		callsAfterLease int64
	}{
		{
			name:            "shipped default releases the key",
			opts:            nil,
			resendStatus:    http.StatusInternalServerError,
			callsAfterLease: 3,
		},
		{
			name:            "fence holds the key for the retention window",
			opts:            []Option{WithServerErrorPolicy(ServerErrorPolicyFence)},
			resendStatus:    http.StatusServiceUnavailable,
			callsAfterLease: 1,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			store, mr := realRedisStore(t)

			// The service's own rewrite of an ambiguous 5xx, the seam a real
			// consumer already wires. Under the fence it also answers the
			// resend, so both requests carry one instruction.
			opts := append([]Option{
				WithKeyTTL(fenceRetention),
				WithProcessingTTL(fenceLease),
				WithPostHandlerUnavailableHandler(func(c fiber.Ctx) error {
					return c.Status(fiber.StatusServiceUnavailable).
						JSON(fiber.Map{"code": "IDEMPOTENCY_OUTCOME_UNKNOWN"})
				}),
			}, testCase.opts...)

			middleware := NewWithStore(store, opts...)

			var calls atomic.Int64

			// A handler cut off by its deadline: the framework writes 500 while
			// the handler is still running and may still commit.
			app := countingMoneyApp(middleware.Check(), "tenant-5xx", &calls, func(c fiber.Ctx) error {
				return c.SendStatus(fiber.StatusInternalServerError)
			})

			first := doPost(t, app, "cutoff-key")
			first.Body.Close()

			require.Equal(t, http.StatusInternalServerError, first.StatusCode)
			require.Equal(t, int64(1), calls.Load())

			// An immediate resend, inside the lease.
			second := doPost(t, app, "cutoff-key")
			secondBody := readBody(t, second)

			assert.Equal(t, testCase.resendStatus, second.StatusCode)

			// After the lease lapses, which is where the released key stops
			// fencing anything at all.
			mr.FastForward(pastTheLease)

			third := doPost(t, app, "cutoff-key")
			third.Body.Close()

			assert.Equal(t, testCase.callsAfterLease, calls.Load(),
				"how many times one idempotency key executed the mutation")

			if testCase.callsAfterLease == 1 {
				assert.Contains(t, secondBody, "IDEMPOTENCY_OUTCOME_UNKNOWN",
					"the fenced resend is answered by the service's own post-handler document")
				assert.Equal(t, http.StatusServiceUnavailable, third.StatusCode)
			}
		})
	}
}

// TestCheck_ServerErrorPolicy_LeavesTheHappyAndConflictPathsAlone pins that the
// fence changes nothing outside the failure branch it was added for. A fenced
// route must still replay a success exactly and still answer an in-flight
// duplicate with 409, or the option bought correctness on one path by breaking
// two others.
func TestCheck_ServerErrorPolicy_LeavesTheHappyAndConflictPathsAlone(t *testing.T) {
	t.Parallel()

	store, _ := realRedisStore(t)
	middleware := NewWithStore(store,
		WithServerErrorPolicy(ServerErrorPolicyFence),
		WithKeyTTL(fenceRetention),
		WithProcessingTTL(time.Hour),
	)

	var calls atomic.Int64

	entered := make(chan struct{})
	release := make(chan struct{})

	app := fiber.New()
	app.Use(tenantMiddleware("tenant-happy"))
	app.Use(middleware.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		calls.Add(1)
		entered <- struct{}{}
		<-release

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	firstStatus := make(chan int, 1)

	go func() {
		response := doPost(t, app, "happy-key")
		defer response.Body.Close()

		firstStatus <- response.StatusCode
	}()

	<-entered

	// Conflict path: the duplicate arrives while the original still holds the
	// lease, and must still be the retryable 409, not the terminal refusal.
	conflict := doPost(t, app, "happy-key")
	conflict.Body.Close()

	assert.Equal(t, http.StatusConflict, conflict.StatusCode)
	assert.Equal(t, retryAfterSeconds, conflict.Header.Get(fiber.HeaderRetryAfter))

	close(release)
	assert.Equal(t, http.StatusCreated, <-firstStatus)

	// Happy path: the completed receipt still replays byte for byte.
	replayed := doPost(t, app, "happy-key")
	body := readBody(t, replayed)

	assert.Equal(t, http.StatusCreated, replayed.StatusCode)
	assert.Contains(t, body, "created")
	assert.Equal(t, "true", replayed.Header.Get(chttp.IdempotencyReplayed))
	assert.Equal(t, int64(1), calls.Load())
}
