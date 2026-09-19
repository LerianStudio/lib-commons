//go:build unit

package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	chttp "github.com/LerianStudio/lib-commons/v7/commons/constants"
	"github.com/LerianStudio/lib-commons/v7/commons/obs"
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

// newKeyedPost builds the POST /test request doPost sends, without doPost's
// require: callers on a spawned goroutine must not abort the test from there.
func newKeyedPost(idempotencyKey string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	req.Header.Set(chttp.IdempotencyKey, idempotencyKey)

	return req
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
	assert.Contains(t, secondBody, "IDEMPOTENCY_OUTCOME_UNRECORDED")
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
	assert.Contains(t, readBody(t, third), "IDEMPOTENCY_OUTCOME_UNRECORDED")
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
	assert.Contains(t, stored, `"outcome":"`+outcomeUnrecorded+`"`)
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
						JSON(fiber.Map{"code": "IDEMPOTENCY_OUTCOME_UNRECORDED"})
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
				assert.Contains(t, secondBody, "IDEMPOTENCY_OUTCOME_UNRECORDED",
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

	// The in-flight request cannot go through doPost: its require.NoError would
	// run on this goroutine, and a failure there calls runtime.Goexit, so
	// nothing would ever be sent and the test would block on <-entered or
	// <-first until the package timeout. The error travels back instead and is
	// asserted on the test goroutine.
	type result struct {
		status int
		err    error
	}

	first := make(chan result, 1)

	go func() {
		response, err := app.Test(newKeyedPost("happy-key"), fiber.TestConfig{Timeout: 0})
		if err != nil {
			first <- result{err: err}

			return
		}

		defer response.Body.Close()

		first <- result{status: response.StatusCode}
	}()

	<-entered

	// Conflict path: the duplicate arrives while the original still holds the
	// lease, and must still be the retryable 409, not the terminal refusal.
	conflict := doPost(t, app, "happy-key")
	conflict.Body.Close()

	assert.Equal(t, http.StatusConflict, conflict.StatusCode)
	assert.Equal(t, retryAfterSeconds, conflict.Header.Get(fiber.HeaderRetryAfter))

	close(release)

	firstResult := <-first
	require.NoError(t, firstResult.err)
	assert.Equal(t, http.StatusCreated, firstResult.status)

	// Happy path: the completed receipt still replays byte for byte.
	replayed := doPost(t, app, "happy-key")
	body := readBody(t, replayed)

	assert.Equal(t, http.StatusCreated, replayed.StatusCode)
	assert.Contains(t, body, "created")
	assert.Equal(t, "true", replayed.Header.Get(chttp.IdempotencyReplayed))
	assert.Equal(t, int64(1), calls.Load())
}

// TestCheck_FencedRecord_IsRefusedByAReaderThatIgnoresTheOutcomeField is the
// rolling-upgrade fence, and it is a money test rather than a compatibility
// nicety.
//
// It is a ROUND TRIP on purpose. It drives the real writer — a handler that
// commits, a completion that fails — reads the bytes that writer actually
// stored, strips the "outcome" field exactly as a middleware predating that
// field drops it while decoding, and feeds the result back. Hand-seeding the
// record instead would pin only the reader, and reverting the writer to a third
// state value would leave this test green while reintroducing the hazard it is
// named for.
//
// Both versions share one store during an upgrade. A third state value would
// reach the unknown-state branch, which under the shipped fail-open default
// calls the handler: the duplicate would execute while the fence sat in the
// store unread. Measured against the pre-change middleware, that shape answered
// 201 and ran the handler a second time, and this shape answered 503 and ran
// nothing.
func TestCheck_FencedRecord_IsRefusedByAReaderThatIgnoresTheOutcomeField(t *testing.T) {
	t.Parallel()

	const key = "idempotency:tenant-upgrade:upgrade-key"

	store, mr := realRedisStore(t)
	writer := NewWithStore(&transientCompleteFailure{Store: store},
		WithKeyTTL(fenceRetention), WithProcessingTTL(fenceLease))

	var written atomic.Int64

	writerApp := countingMoneyApp(writer.Check(), "tenant-upgrade", &written, func(c fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "payoff armed"})
	})

	doPost(t, writerApp, "upgrade-key").Body.Close()
	require.Equal(t, int64(1), written.Load())

	stored, err := mr.Get(key)
	require.NoError(t, err, "the writer must have left a record to upgrade across")

	// What the writer stored has to be routable by a reader that predates the
	// marker, which routes on state alone.
	require.Contains(t, stored, `"state":"`+keyStateComplete+`"`,
		"a state an older reader does not know sends it to the fail-open branch, which re-executes")
	require.Contains(t, stored, `"outcome":"`+outcomeUnrecorded+`"`)

	// Decode and re-encode without the marker: byte for byte what an older
	// middleware's storeRecord holds after json.Unmarshal drops the field.
	var asOldReaderSeesIt map[string]any

	require.NoError(t, json.Unmarshal([]byte(stored), &asOldReaderSeesIt))
	delete(asOldReaderSeesIt, "outcome")

	downgraded, err := json.Marshal(asOldReaderSeesIt)
	require.NoError(t, err)
	require.NoError(t, mr.Set(key, string(downgraded)))

	var calls atomic.Int64

	readerApp := countingMoneyApp(NewWithStore(store).Check(), "tenant-upgrade", &calls, func(c fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "executed again"})
	})

	response := doPost(t, readerApp, "upgrade-key")
	body := readBody(t, response)

	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	assert.Contains(t, body, "reconcile the original request first")
	assert.Equal(t, int64(0), calls.Load(),
		"a reader that cannot see the marker must still refuse, never re-run the operation")
}

// alwaysFailingComplete fails EVERY Complete: the receipt write and the fence
// that follows it. This is the total-outage case the fence explicitly cannot
// close, so the contract under test is not that the key survives — it does not
// — but that the response says so.
type alwaysFailingComplete struct {
	Store

	completes atomic.Int64
}

func (s *alwaysFailingComplete) Complete(
	_ context.Context, _ string, _, _ []byte, _ time.Duration,
) (bool, error) {
	s.completes.Add(1)

	return false, errReceiptWrite
}

// recordingLogger captures log lines so a test can assert on the one that wakes
// an operator at 3am. The package tests no other logs; this one is load-bearing
// because it is the ONLY per-key trace of an unfenced money request.
type recordingLogger struct {
	mu    sync.Mutex
	lines []loggedLine
}

type loggedLine struct {
	level int
	msg   string
	kv    map[string]any
}

func (l *recordingLogger) Log(_ context.Context, level int, msg string, kv ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	fields := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		name, ok := kv[i].(string)
		if !ok {
			continue
		}

		fields[name] = kv[i+1]
	}

	l.lines = append(l.lines, loggedLine{level: level, msg: msg, kv: fields})
}

func (l *recordingLogger) Enabled(int) bool           { return true }
func (l *recordingLogger) Sync(context.Context) error { return nil }

func (l *recordingLogger) find(t *testing.T, level int, substring string) loggedLine {
	t.Helper()

	l.mu.Lock()
	defer l.mu.Unlock()

	for _, line := range l.lines {
		if line.level == level && strings.Contains(line.msg, substring) {
			return line
		}
	}

	t.Fatalf("no log line at level %d containing %q; got %+v", level, substring, l.lines)

	return loggedLine{}
}

// TestCheck_FenceFailure_SaysTheKeyIsUnprotected covers the case the fence
// cannot close: the store is down for the receipt AND for the fence.
//
// Best-effort is fine. Answering as if it had worked is not. Without a separate
// document the caller gets the same "reconcile the original request" whether
// the key is held or free, and a client that resends is refused in one case and
// arms the operation a SECOND time in the other, with nothing in the response
// telling it which. Here the key really is free — measured below, the resend
// executes — so the first answer has to say so.
func TestCheck_FenceFailure_SaysTheKeyIsUnprotected(t *testing.T) {
	t.Parallel()

	store, mr := realRedisStore(t)
	logger := &recordingLogger{}

	middleware := NewWithStore(&alwaysFailingComplete{Store: store},
		WithKeyTTL(fenceRetention),
		WithProcessingTTL(fenceLease),
		WithLogger(logger),
		// The seam a real consumer wires for the FENCED case. It must not
		// answer this one, or the two become indistinguishable again.
		WithPostHandlerUnavailableHandler(func(c fiber.Ctx) error {
			return c.Status(fiber.StatusServiceUnavailable).
				JSON(fiber.Map{"code": "SERVICE_RECEIPT_LOST"})
		}),
	)

	var calls atomic.Int64

	app := countingMoneyApp(middleware.Check(), "tenant-unfenced", &calls, func(c fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "payoff armed"})
	})

	response := doPost(t, app, "unfenced-key")
	body := readBody(t, response)

	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	assert.Contains(t, body, "IDEMPOTENCY_UNFENCED")
	assert.NotContains(t, body, "SERVICE_RECEIPT_LOST",
		"the seam wired for a fenced key must not answer an unfenced one")
	assert.Contains(t, body, "NOT protected")
	assert.Equal(t, "false", response.Header.Get(chttp.IdempotencyFenced),
		"machine-readable, and the only signal the handler-failure branch can carry")
	assert.Empty(t, response.Header.Get(fiber.HeaderRetryAfter),
		"retrying is exactly what the caller must not do before reconciling")

	// The alert an operator is woken by has to name the money request.
	line := logger.find(t, obs.LevelError, "UNFENCED")

	assert.Equal(t, keyDigest("idempotency:tenant-unfenced:unfenced-key"), line.kv["idempotency_key_digest"],
		"the digest must be reproducible from the key the client holds")
	assert.Equal(t, "tenant-unfenced", line.kv["tenant_id"])
	assert.NotEmpty(t, line.kv["owner"])

	// And the honesty is warranted. The residual is exactly the shape this PR
	// removes everywhere the fence DOES land: the key still holds the processing
	// record, so an immediate resend meets the false in-flight...
	assert.Equal(t, int64(1), calls.Load())

	immediate := doPost(t, app, "unfenced-key")
	immediate.Body.Close()

	assert.Equal(t, http.StatusConflict, immediate.StatusCode,
		"nothing is in flight — the handler returned — but without a fence the lease is all that is left")

	// ...and once that lease lapses the key is free and the operation runs a
	// second time. Unpreventable here by construction: writing anything durable
	// is what failed. Which is the whole reason the first answer had to say the
	// key was not protected.
	mr.FastForward(pastTheLease)

	doPost(t, app, "unfenced-key").Body.Close()
	assert.Equal(t, int64(2), calls.Load(),
		"the store never took the fence, so the resend runs — which is why the first answer said so")
}

// TestCheck_FenceSuccess_SaysTheKeyIsHeld is the other half of the pair. Same
// shape, a store that fails only the receipt, and every signal inverts.
func TestCheck_FenceSuccess_SaysTheKeyIsHeld(t *testing.T) {
	t.Parallel()

	store, _ := realRedisStore(t)
	logger := &recordingLogger{}

	middleware := NewWithStore(&transientCompleteFailure{Store: store},
		WithKeyTTL(fenceRetention),
		WithProcessingTTL(fenceLease),
		WithLogger(logger),
		WithPostHandlerUnavailableHandler(func(c fiber.Ctx) error {
			return c.Status(fiber.StatusServiceUnavailable).
				JSON(fiber.Map{"code": "SERVICE_RECEIPT_LOST"})
		}),
	)

	var calls atomic.Int64

	app := countingMoneyApp(middleware.Check(), "tenant-fenced", &calls, func(c fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "payoff armed"})
	})

	response := doPost(t, app, "fenced-key")
	body := readBody(t, response)

	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	assert.Contains(t, body, "SERVICE_RECEIPT_LOST",
		"a fenced key is the case the post-handler seam exists for")
	assert.NotContains(t, body, "IDEMPOTENCY_UNFENCED")
	assert.Equal(t, "true", response.Header.Get(chttp.IdempotencyFenced))
	assert.Empty(t, response.Header.Get(fiber.HeaderRetryAfter))

	logger.find(t, obs.LevelWarn, "key fenced with an unrecorded outcome")

	assert.Equal(t, int64(1), calls.Load())
	doPost(t, app, "fenced-key").Body.Close()
	assert.Equal(t, int64(1), calls.Load(), "the fence holds, so the resend does not run")
}

// TestCheck_FenceFailure_UnfencedHandlerAnswersWhenWired covers the seam this
// case has of its own.
//
// The built-in refusal is right for a route whose handler can be re-run: the
// key is free, so "reconcile before resending" is the only safe instruction.
// It is wrong for a route whose handler already committed something the
// service cannot take back — an averbação accepted by a rail, a bid placed —
// because reporting failure for an operation that SUCCEEDED is itself what
// makes the client resend, which is the double execution this package exists
// to prevent. Only the route knows which of the two it is, so the answer is
// handed there while the header keeps telling the truth about the key.
func TestCheck_FenceFailure_UnfencedHandlerAnswersWhenWired(t *testing.T) {
	t.Parallel()

	store, _ := realRedisStore(t)
	faulty := &alwaysFailingComplete{Store: store}

	middleware := NewWithStore(faulty,
		WithKeyTTL(fenceRetention),
		WithProcessingTTL(fenceLease),
		// Both post-handler seams are wired. Only the unfenced one may answer.
		WithPostHandlerUnavailableHandler(func(c fiber.Ctx) error {
			return c.Status(fiber.StatusServiceUnavailable).
				JSON(fiber.Map{"code": "SERVICE_RECEIPT_LOST"})
		}),
		WithUnfencedHandler(func(c fiber.Ctx) error {
			return c.Status(fiber.StatusCreated).
				JSON(fiber.Map{"code": "SERVICE_COMMITTED_UNPROTECTED"})
		}),
	)

	var calls atomic.Int64

	app := countingMoneyApp(middleware.Check(), "tenant-unfenced-seam", &calls, func(c fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "payoff armed"})
	})

	response := doPost(t, app, "unfenced-seam-key")
	body := readBody(t, response)

	assert.Equal(t, http.StatusCreated, response.StatusCode,
		"once the route wires the seam, the route owns what its client hears")
	assert.Contains(t, body, "SERVICE_COMMITTED_UNPROTECTED")
	assert.NotContains(t, body, "IDEMPOTENCY_UNFENCED",
		"the built-in refusal is the default, not a floor the seam sits under")
	assert.NotContains(t, body, "SERVICE_RECEIPT_LOST",
		"the seam wired for a fenced key must still not answer an unfenced one")
	assert.Equal(t, "false", response.Header.Get(chttp.IdempotencyFenced),
		"the key is unprotected whatever the handler answers, including under a 2xx")

	assert.Equal(t, int64(1), calls.Load())
	assert.Equal(t, int64(2), faulty.completes.Load(),
		"this is the two-failure case: the receipt write, then the fence write")
}

// TestCheck_FenceSuccess_DoesNotCallTheUnfencedHandler holds the two apart from
// the other side. A fence that LANDED is not this seam's case: the key is held,
// a resend is refused, and a route that asked to answer for an UNPROTECTED key
// must never be handed a protected one — it would report "unprotected, go
// reconcile" about a key that is doing its job.
func TestCheck_FenceSuccess_DoesNotCallTheUnfencedHandler(t *testing.T) {
	t.Parallel()

	store, _ := realRedisStore(t)

	var unfenced atomic.Int64

	middleware := NewWithStore(&transientCompleteFailure{Store: store},
		WithKeyTTL(fenceRetention),
		WithProcessingTTL(fenceLease),
		WithPostHandlerUnavailableHandler(func(c fiber.Ctx) error {
			return c.Status(fiber.StatusServiceUnavailable).
				JSON(fiber.Map{"code": "SERVICE_RECEIPT_LOST"})
		}),
		WithUnfencedHandler(func(c fiber.Ctx) error {
			unfenced.Add(1)

			return c.Status(fiber.StatusCreated).
				JSON(fiber.Map{"code": "SERVICE_COMMITTED_UNPROTECTED"})
		}),
	)

	var calls atomic.Int64

	app := countingMoneyApp(middleware.Check(), "tenant-fenced-seam", &calls, func(c fiber.Ctx) error {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "payoff armed"})
	})

	response := doPost(t, app, "fenced-seam-key")
	body := readBody(t, response)

	assert.Equal(t, int64(0), unfenced.Load(),
		"the fence landed, so this is not the unfenced seam's case")
	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	assert.Contains(t, body, "SERVICE_RECEIPT_LOST")
	assert.NotContains(t, body, "SERVICE_COMMITTED_UNPROTECTED")
	assert.Equal(t, "true", response.Header.Get(chttp.IdempotencyFenced))
	assert.Equal(t, int64(1), calls.Load())
}

// TestCheck_UnrecognisedRecordState_IsRefusedEvenWhenFailOpen is the
// mixed-version fence pointed FORWARD.
//
// The rest of this change makes a record THIS version writes safe for an older
// reader. This is the other direction: a record a FUTURE version writes must not
// make this reader execute. Before the refusal, an existing record carrying an
// unknown state reached the store-error path, whose fail-open default — the one
// every consumer of New(conn) gets — calls the handler. So the next new state
// value anyone adds would reintroduce the exact double execution this package
// was changed to prevent, on the older half of every rolling upgrade.
//
// Fail-open is right when the middleware learned nothing. Here it learned that
// the key demonstrably holds somebody's record, so the refusal is unconditional
// in BOTH constructors, and nothing is written so the record survives for a
// reader that does understand it.
func TestCheck_UnrecognisedRecordState_IsRefusedEvenWhenFailOpen(t *testing.T) {
	t.Parallel()

	const (
		key         = "idempotency:tenant-future:future-key"
		futureState = "fenced-by-some-later-version"
	)

	tests := []struct {
		name       string
		middleware func(t *testing.T, mr *miniredis.Miniredis, logger obs.Logger) *Middleware
	}{
		{
			// The load-bearing row: this is the shipped default, and it is the
			// configuration that used to execute.
			name: "New, the fail-OPEN shipped default",
			middleware: func(t *testing.T, mr *miniredis.Miniredis, logger obs.Logger) *Middleware {
				return New(newRedisClient(t, mr), WithLogger(logger))
			},
		},
		{
			name: "NewWithStore, fail-closed",
			middleware: func(t *testing.T, mr *miniredis.Miniredis, logger obs.Logger) *Middleware {
				return NewWithStore(newRedisStore(newRedisClient(t, mr)), WithLogger(logger))
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			mr := miniredis.RunT(t)
			logger := &recordingLogger{}

			seeded := storeRecord{
				State:       futureState,
				Fingerprint: requestFingerprint(http.MethodPost, "/test", nil),
				Owner:       "owner-from-the-future",
			}
			seedStoreRecord(t, mr, key, seeded)

			before, err := mr.Get(key)
			require.NoError(t, err)

			var calls atomic.Int64

			app := countingMoneyApp(testCase.middleware(t, mr, logger).Check(), "tenant-future", &calls,
				func(c fiber.Ctx) error {
					return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "executed again"})
				})

			response := doPost(t, app, "future-key")
			body := readBody(t, response)

			assert.Equal(t, int64(0), calls.Load(),
				"the key already holds somebody's record; running on top of it is the duplicate this package prevents")
			assert.Equal(t, http.StatusUnprocessableEntity, response.StatusCode)
			assert.Contains(t, body, "IDEMPOTENCY_STATE_UNRECOGNISED")
			assert.Empty(t, response.Header.Get(fiber.HeaderRetryAfter),
				"waiting does not teach this instance a state it does not have")

			after, err := mr.Get(key)
			require.NoError(t, err)
			assert.Equal(t, before, after,
				"the record must survive untouched for a reader that understands it")

			line := logger.find(t, obs.LevelError, "does not recognise")
			assert.Equal(t, futureState, line.kv["record_state"])
		})
	}
}

// TestCheck_UnreadableRecord_IsRefusedEvenWhenFailOpen closes the last
// fail-open-on-an-existing-record path in the package.
//
// The discriminator was never whether the bytes parse. Acquire returned
// acquired=false, which is proof the key is occupied by a live record with an
// unexpired TTL; corrupt bytes say nothing about that, and leave this reader
// with strictly LESS information than an unrecognised state, not more. Before
// this, all three shapes below ran the operation under New(conn) — the
// constructor the README calls the default.
//
// decodeLegacyRecord, three arms above, is deliberately closed for this exact
// reason: "unknown bytes granting permission to answer a mutation without
// running it, or worse, to run it a second time". This is where its rejects
// land, so landing them anywhere that executes would defeat that detector.
func TestCheck_UnreadableRecord_IsRefusedEvenWhenFailOpen(t *testing.T) {
	t.Parallel()

	const key = "idempotency:tenant-corrupt:corrupt-key"

	shapes := []struct {
		name   string
		stored string
	}{
		{"not json at all", `}{ garbage not json`},
		{"truncated mid-record", `{"state":"complete","fingerp`},
		{"json, but not an object", `["state","complete"]`},
	}

	policies := []struct {
		name string
		opts []Option
	}{
		// The load-bearing row: the shipped default, and the one that executed.
		{"New, the fail-OPEN shipped default", nil},
		{"New with FailClosed", []Option{WithFailClosed(true)}},
	}

	for _, shape := range shapes {
		for _, policy := range policies {
			t.Run(shape.name+"/"+policy.name, func(t *testing.T) {
				t.Parallel()

				mr := miniredis.RunT(t)
				logger := &recordingLogger{}
				require.NoError(t, mr.Set(key, shape.stored))

				opts := append([]Option{WithLogger(logger)}, policy.opts...)

				var calls atomic.Int64

				app := countingMoneyApp(New(newRedisClient(t, mr), opts...).Check(), "tenant-corrupt", &calls,
					func(c fiber.Ctx) error {
						return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "executed again"})
					})

				response := doPost(t, app, "corrupt-key")
				body := readBody(t, response)

				assert.Equal(t, int64(0), calls.Load(),
					"the key is occupied by a live record; running on top of it is the duplicate this package prevents")
				assert.Equal(t, http.StatusUnprocessableEntity, response.StatusCode)
				assert.Contains(t, body, "IDEMPOTENCY_RECORD_UNREADABLE")
				assert.NotContains(t, body, "IDEMPOTENCY_STATE_UNRECOGNISED",
					"damaged bytes are not version skew, and an operator triaging the two must tell them apart")

				after, err := mr.Get(key)
				require.NoError(t, err)
				assert.Equal(t, shape.stored, after, "the bytes must survive for inspection")

				line := logger.find(t, obs.LevelError, "could not be decoded")
				assert.Equal(t, keyDigest(key), line.kv["idempotency_key_digest"])
				assert.Equal(t, "tenant-corrupt", line.kv["tenant_id"])
			})
		}
	}
}

// TestCheck_ServerErrorPolicyFence_AlwaysReportsWhetherTheKeyIsHeld covers the
// one branch where the header is the SOLE carrier.
//
// Under the Fence policy the middleware must return the handler's error so the
// application's error handler owns the response; it writes no document of its
// own, so nothing but [constants.IdempotencyFenced] can say whether the fence
// landed. The other header assertions in this file sit on the
// completion-failure path, where the body carries the same fact — they would
// stay green if this branch reported the wrong value, which is precisely the
// mutation this test exists to catch.
func TestCheck_ServerErrorPolicyFence_AlwaysReportsWhetherTheKeyIsHeld(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// wrap decides whether the fence write can land.
		wrap       func(Store) Store
		wantFenced string
		// callsAfterLease is the fact the header is reporting.
		callsAfterLease int64
	}{
		{
			name:            "fence lands",
			wrap:            func(s Store) Store { return s },
			wantFenced:      "true",
			callsAfterLease: 1,
		},
		{
			// 2, not 3: the fence never landed, but the processing record it
			// failed to replace is still there, so the immediate resend meets
			// the false in-flight and only the one after the lease executes.
			// Which is the whole point of the header — that leftover lease is
			// not a fence and expires without one.
			name:            "fence write fails",
			wrap:            func(s Store) Store { return &alwaysFailingComplete{Store: s} },
			wantFenced:      "false",
			callsAfterLease: 2,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			store, mr := realRedisStore(t)
			middleware := NewWithStore(testCase.wrap(store),
				WithServerErrorPolicy(ServerErrorPolicyFence),
				WithKeyTTL(fenceRetention),
				WithProcessingTTL(fenceLease),
			)

			var calls atomic.Int64

			// A handler cut off by its deadline: the framework answers 500
			// while the handler is still running and may still commit.
			app := countingMoneyApp(middleware.Check(), "tenant-fence-header", &calls, func(c fiber.Ctx) error {
				return c.SendStatus(fiber.StatusInternalServerError)
			})

			first := doPost(t, app, "fence-header-key")
			first.Body.Close()

			require.Equal(t, http.StatusInternalServerError, first.StatusCode,
				"this branch must leave the response to the application's error handler")
			assert.Equal(t, testCase.wantFenced, first.Header.Get(chttp.IdempotencyFenced),
				"the only thing that can say whether a resend is refused or arms the operation again")

			// And the header has to be telling the truth.
			doPost(t, app, "fence-header-key").Body.Close()
			mr.FastForward(pastTheLease)
			doPost(t, app, "fence-header-key").Body.Close()

			assert.Equal(t, testCase.callsAfterLease, calls.Load(),
				"how many times one idempotency key executed the operation")
		})
	}
}
