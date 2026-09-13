//go:build unit

package idempotency

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// expiringStore is a TTL-honouring in-memory Store. The mock store cannot serve
// these tests: the defect under test is a record DISAPPEARING while its handler
// still runs, so the store has to age its records on a real clock rather than
// replay a scripted sequence of answers.
type expiringStore struct {
	mu      sync.Mutex
	records map[string]expiringRecord
}

type expiringRecord struct {
	value     []byte
	expiresAt time.Time
}

func newExpiringStore() *expiringStore {
	return &expiringStore{records: make(map[string]expiringRecord)}
}

func (s *expiringStore) Acquire(
	_ context.Context, key string, candidate []byte, ttl time.Duration,
) ([]byte, bool, error) {
	if ttl <= 0 {
		return nil, false, errors.New("idempotency test store: TTL must be positive")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if record, found := s.live(key); found {
		return append([]byte(nil), record.value...), false, nil
	}

	s.records[key] = expiringRecord{
		value:     append([]byte(nil), candidate...),
		expiresAt: time.Now().Add(ttl),
	}

	return nil, true, nil
}

func (s *expiringStore) Complete(
	_ context.Context, key string, expected, completed []byte, ttl time.Duration,
) (bool, error) {
	if ttl <= 0 {
		return false, errors.New("idempotency test store: TTL must be positive")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, found := s.live(key)
	if !found || !bytes.Equal(record.value, expected) {
		return false, nil
	}

	s.records[key] = expiringRecord{
		value:     append([]byte(nil), completed...),
		expiresAt: time.Now().Add(ttl),
	}

	return true, nil
}

func (s *expiringStore) Release(_ context.Context, key string, expected []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, found := s.live(key)
	if !found || !bytes.Equal(record.value, expected) {
		return false, nil
	}

	delete(s.records, key)

	return true, nil
}

// live reads a record and drops it when its TTL has passed. Callers hold s.mu.
func (s *expiringStore) live(key string) (expiringRecord, bool) {
	record, found := s.records[key]
	if found && time.Now().After(record.expiresAt) {
		delete(s.records, key)

		return expiringRecord{}, false
	}

	return record, found
}

// blockingApp builds a POST /test app whose handler announces itself on entered
// and then waits for release before returning 201, so a test can hold a request
// in flight without sleeping.
func blockingApp(mw fiber.Handler, tenantID string, calls *atomic.Int64, entered chan<- struct{}, release <-chan struct{}) *fiber.App {
	app := fiber.New()
	app.Use(tenantMiddleware(tenantID))
	app.Use(mw)
	app.Post("/test", func(c fiber.Ctx) error {
		calls.Add(1)
		entered <- struct{}{}
		<-release

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	return app
}

// TestCheck_ProcessingTTL_SeparatesLeaseFromRetention pins which of the two
// lifetimes reaches which store call: the in-flight lease goes to Acquire, the
// replay retention goes to Complete.
//
// The no-option rows are the load-bearing ones. They pin that an unset
// processing TTL still sends the retention TTL to BOTH calls, which is the
// shipped behaviour every existing caller depends on.
func TestCheck_ProcessingTTL_SeparatesLeaseFromRetention(t *testing.T) {
	t.Parallel()

	const retention = time.Hour

	tests := []struct {
		name         string
		opts         []Option
		wantAcquire  time.Duration
		wantComplete time.Duration
	}{
		{
			name:         "lease shorter than retention",
			opts:         []Option{WithKeyTTL(retention), WithProcessingTTL(50 * time.Millisecond)},
			wantAcquire:  50 * time.Millisecond,
			wantComplete: retention,
		},
		{
			name:         "lease longer than retention",
			opts:         []Option{WithKeyTTL(5 * time.Minute), WithProcessingTTL(30 * time.Minute)},
			wantAcquire:  30 * time.Minute,
			wantComplete: 5 * time.Minute,
		},
		{
			name:         "unset lease borrows the retention",
			opts:         []Option{WithKeyTTL(retention)},
			wantAcquire:  retention,
			wantComplete: retention,
		},
		{
			name:         "zero is ignored",
			opts:         []Option{WithKeyTTL(retention), WithProcessingTTL(0)},
			wantAcquire:  retention,
			wantComplete: retention,
		},
		{
			name:         "negative is ignored",
			opts:         []Option{WithKeyTTL(retention), WithProcessingTTL(-time.Second)},
			wantAcquire:  retention,
			wantComplete: retention,
		},
		{
			name: "provider governs retention only",
			opts: []Option{
				WithTTLProvider(func(_ fiber.Ctx) (time.Duration, error) {
					return 90 * time.Second, nil
				}),
				WithProcessingTTL(50 * time.Millisecond),
			},
			wantAcquire:  50 * time.Millisecond,
			wantComplete: 90 * time.Second,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			controller := gomock.NewController(t)
			store := NewMockStore(controller)

			var acquireTTL, completeTTL time.Duration

			store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, _ string, _ []byte, ttl time.Duration) ([]byte, bool, error) {
					acquireTTL = ttl

					return nil, true, nil
				})
			store.EXPECT().Complete(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, _ string, _, _ []byte, ttl time.Duration) (bool, error) {
					completeTTL = ttl

					return true, nil
				})

			middleware := NewWithStore(store, testCase.opts...)

			var calls atomic.Int64

			response := doPost(t, countingApp(middleware.Check(), "tenant-lease", &calls), "lease-key")
			response.Body.Close()

			assert.Equal(t, http.StatusCreated, response.StatusCode)
			assert.Equal(t, int64(1), calls.Load())
			assert.Equal(t, testCase.wantAcquire, acquireTTL, "the in-flight lease goes to Acquire")
			assert.Equal(t, testCase.wantComplete, completeTTL, "the replay retention goes to Complete")
		})
	}
}

// TestCheck_ProcessingTTL_ShortLeaseLetsTheHandlerRunTwice reproduces the defect
// [WithProcessingTTL] exists to prevent, so the fix can be shown to close it.
//
// With one TTL for both lifetimes, a caller choosing a short replay-retention
// window also caps its handlers: the lease expires mid-flight, the first
// request's completion is rejected because the record it owned is gone (503),
// and a redelivery under the SAME key acquires it again and runs the handler a
// SECOND time. On a money route that second run is a duplicated movement.
func TestCheck_ProcessingTTL_ShortLeaseLetsTheHandlerRunTwice(t *testing.T) {
	t.Parallel()

	const (
		lease          = 50 * time.Millisecond
		handlerRuntime = 150 * time.Millisecond
	)

	store := newExpiringStore()
	middleware := NewWithStore(store, WithKeyTTL(time.Hour), WithProcessingTTL(lease))

	var calls atomic.Int64

	app := fiber.New()
	app.Use(tenantMiddleware("tenant-expiry"))
	app.Use(middleware.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		calls.Add(1)
		time.Sleep(handlerRuntime) // outlives the lease

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	first := doPost(t, app, "expiring-key")
	first.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, first.StatusCode,
		"the lease expired mid-flight, so completion is rejected")

	second := doPost(t, app, "expiring-key")
	second.Body.Close()

	// The second request loses its own lease the same way, so it also answers
	// 503. The COUNT is the finding: the key protected nothing.
	assert.Equal(t, http.StatusServiceUnavailable, second.StatusCode)
	assert.Equal(t, int64(2), calls.Load(),
		"an expired lease lets the same key execute the mutation twice")
}

// TestCheck_ProcessingTTL_LongLeaseHoldsTheKeyInFlight is the same scenario with
// a lease that outlives the handler: the duplicate is refused with 409 and the
// mutation runs exactly once.
func TestCheck_ProcessingTTL_LongLeaseHoldsTheKeyInFlight(t *testing.T) {
	t.Parallel()

	store := newExpiringStore()
	// A short retention with a long lease: the combination the option unlocks.
	middleware := NewWithStore(store, WithKeyTTL(5*time.Minute), WithProcessingTTL(time.Hour))

	var calls atomic.Int64

	entered := make(chan struct{})
	release := make(chan struct{})
	app := blockingApp(middleware.Check(), "tenant-inflight", &calls, entered, release)

	firstStatus := make(chan int, 1)

	go func() {
		response := doPost(t, app, "inflight-key")
		defer response.Body.Close()

		firstStatus <- response.StatusCode
	}()

	<-entered // the first request now holds the lease

	second := doPost(t, app, "inflight-key")
	second.Body.Close()

	assert.Equal(t, http.StatusConflict, second.StatusCode,
		"a live lease refuses the duplicate instead of re-running it")

	close(release)

	assert.Equal(t, http.StatusCreated, <-firstStatus)
	assert.Equal(t, int64(1), calls.Load(), "the mutation must run exactly once")
}

// TestCheck_PostHandlerUnavailable_RoutesByWhetherTheHandlerRan pins that the
// two store failures reach two different seams.
//
// They carry opposite instructions. A failure BEFORE the handler means nothing
// ran and retrying is correct; one AFTER means the side effect is committed and
// a retry under a new key duplicates it. One shared override cannot say which.
func TestCheck_PostHandlerUnavailable_RoutesByWhetherTheHandlerRan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// arrange drives the store into one of the two failures.
		arrange func(store *MockStore)
		opts    []Option
		// installPost decides whether the post-handler seam is configured at
		// all. The cases that install it and still expect the PRE-handler seam
		// are the discriminating ones: without them a case cannot tell the two
		// routings apart, because an unset post seam falls back to the pre one.
		installPost bool
		wantPre     bool
		wantPost    bool
		wantHandler bool
		wantStatus  int
	}{
		{
			name: "completion failure reaches the post-handler seam",
			arrange: func(store *MockStore) {
				store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(nil, true, nil)
				store.EXPECT().Complete(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(false, errors.New("backend unavailable"))
			},
			installPost: true,
			wantPost:    true,
			wantHandler: true,
			wantStatus:  http.StatusTeapot,
		},
		{
			name: "acquisition failure reaches the pre-handler seam",
			arrange: func(store *MockStore) {
				store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(nil, false, errors.New("backend unavailable"))
			},
			installPost: true,
			wantPre:     true,
			wantStatus:  http.StatusIMUsed,
		},
		{
			name: "TTL provider failure reaches the pre-handler seam",
			arrange: func(_ *MockStore) {
				// Nothing reaches the store: the TTL is resolved first.
			},
			opts: []Option{WithTTLProvider(func(_ fiber.Ctx) (time.Duration, error) {
				return 0, errors.New("policy lookup failed")
			})},
			installPost: true,
			wantPre:     true,
			wantStatus:  http.StatusIMUsed,
		},
		{
			name: "post-handler seam unset falls back to the shared one",
			arrange: func(store *MockStore) {
				store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(nil, true, nil)
				store.EXPECT().Complete(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(false, errors.New("backend unavailable"))
			},
			wantPre:     true,
			wantHandler: true,
			wantStatus:  http.StatusIMUsed,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			controller := gomock.NewController(t)
			store := NewMockStore(controller)
			testCase.arrange(store)

			var preCalled, postCalled atomic.Bool

			opts := append([]Option{
				WithUnavailableHandler(func(c fiber.Ctx) error {
					preCalled.Store(true)

					return c.SendStatus(http.StatusIMUsed)
				}),
			}, testCase.opts...)

			if testCase.installPost {
				opts = append(opts, WithPostHandlerUnavailableHandler(func(c fiber.Ctx) error {
					postCalled.Store(true)

					return c.SendStatus(http.StatusTeapot)
				}))
			}

			middleware := NewWithStore(store, opts...)

			var calls atomic.Int64

			response := doPost(t, countingApp(middleware.Check(), "tenant-seam", &calls), "seam-key")
			response.Body.Close()

			assert.Equal(t, testCase.wantStatus, response.StatusCode)
			assert.Equal(t, testCase.wantPre, preCalled.Load(), "pre-handler seam")
			assert.Equal(t, testCase.wantPost, postCalled.Load(), "post-handler seam")

			wantCalls := int64(0)
			if testCase.wantHandler {
				wantCalls = 1
			}

			assert.Equal(t, wantCalls, calls.Load())
		})
	}
}

// TestCheck_UnavailableDefaults_DifferInRetryGuidance pins the built-in bodies
// an operator reads when neither seam is overridden. Both are 503
// IDEMPOTENCY_UNAVAILABLE, and they must NOT say the same thing: the
// post-handler one has to forbid the retry the pre-handler one invites.
func TestCheck_UnavailableDefaults_DifferInRetryGuidance(t *testing.T) {
	t.Parallel()

	preHandler := func() *http.Response {
		controller := gomock.NewController(t)
		store := NewMockStore(controller)
		store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, false, errors.New("backend unavailable"))

		var calls atomic.Int64

		return doPost(t, countingApp(NewWithStore(store).Check(), "tenant-default", &calls), "default-pre")
	}()

	postHandler := func() *http.Response {
		controller := gomock.NewController(t)
		store := NewMockStore(controller)
		store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, true, nil)
		store.EXPECT().Complete(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(false, errors.New("backend unavailable"))

		var calls atomic.Int64

		return doPost(t, countingApp(NewWithStore(store).Check(), "tenant-default", &calls), "default-post")
	}()

	require.Equal(t, http.StatusServiceUnavailable, preHandler.StatusCode)
	require.Equal(t, http.StatusServiceUnavailable, postHandler.StatusCode)

	preBody := decodeErrorBody(t, preHandler)
	postBody := decodeErrorBody(t, postHandler)

	assert.Equal(t, "IDEMPOTENCY_UNAVAILABLE", preBody.Title)
	assert.Equal(t, "IDEMPOTENCY_UNAVAILABLE", postBody.Title)
	assert.NotEqual(t, preBody.Message, postBody.Message,
		"the two failures must not read identically to an operator")
	assert.NotContains(t, preBody.Message, "do not retry",
		"nothing ran, so the caller may retry the same request")
	assert.Contains(t, postBody.Message, "do not retry",
		"the mutation is committed; a new key would duplicate it")
	assert.Contains(t, postBody.Message, "reconcile")
}
