//go:build unit

package idempotency

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// TestCheck_ClientErrorPolicyFunc_DecidesPerResponse covers the case the enum
// cannot serve: the guard is mounted ABOVE a global rate limiter and a per-route
// quota gate, so some of the 4xx it observes were never the handler's answer.
//
// Under [ClientErrorPolicyCache] a transient 429 spends the caller's key and is
// replayed for the whole retention window; under [ClientErrorPolicyRelease] a
// genuine validation rejection stops being cached. One key is one attempt for
// the handler's own answers, and a refusal written below the guard is not one of
// them — only the route owner can tell those apart, per response.
func TestCheck_ClientErrorPolicyFunc_DecidesPerResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// withFunc installs the per-response seam. The rows that leave it off
		// are the load-bearing ones: they pin that the enum path is untouched.
		withFunc bool
		opts     []Option
		// handlerStatus is what the chain below the guard writes.
		handlerStatus int
		// wantReleased is the observable: a released key can run again, a kept
		// one is completed and replayed.
		wantReleased bool
		// wantSeen is every status the func was consulted with.
		wantSeen []int
		// returns, when set, is what the func answers instead of the consumer
		// rule below — the lane for a value that is neither constant.
		returns *ClientErrorPolicy
	}{
		{
			name:          "func releases a refusal written below the guard",
			withFunc:      true,
			handlerStatus: http.StatusTooManyRequests,
			wantReleased:  true,
			wantSeen:      []int{http.StatusTooManyRequests},
		},
		{
			name:          "func keeps the handler's own rejection",
			withFunc:      true,
			handlerStatus: http.StatusUnprocessableEntity,
			wantReleased:  false,
			wantSeen:      []int{http.StatusUnprocessableEntity},
		},
		{
			name:          "func is never consulted for a success",
			withFunc:      true,
			handlerStatus: http.StatusCreated,
			wantReleased:  false,
			wantSeen:      nil,
		},
		{
			name:          "func overrides the cache enum",
			withFunc:      true,
			opts:          []Option{WithClientErrorPolicy(ClientErrorPolicyCache)},
			handlerStatus: http.StatusPaymentRequired,
			wantReleased:  true,
			wantSeen:      []int{http.StatusPaymentRequired},
		},
		{
			name:          "func overrides the release enum",
			withFunc:      true,
			opts:          []Option{WithClientErrorPolicy(ClientErrorPolicyRelease)},
			handlerStatus: http.StatusUnprocessableEntity,
			wantReleased:  false,
			wantSeen:      []int{http.StatusUnprocessableEntity},
		},
		{
			name:          "release enum stands when no func is set",
			opts:          []Option{WithClientErrorPolicy(ClientErrorPolicyRelease)},
			handlerStatus: http.StatusUnprocessableEntity,
			wantReleased:  true,
		},
		{
			name:          "cache default stands when no func is set",
			handlerStatus: http.StatusTooManyRequests,
			wantReleased:  false,
		},
		{
			// Neither constant. The enum option validates and ignores such a
			// value; the func has no such lane, so it reads as the documented
			// default rather than as a third behaviour.
			name:          "an out-of-range return reads as the cache default",
			withFunc:      true,
			opts:          []Option{WithClientErrorPolicy(ClientErrorPolicyRelease)},
			handlerStatus: http.StatusUnprocessableEntity,
			returns:       clientPolicyPtr(ClientErrorPolicy(9)),
			wantReleased:  false,
			wantSeen:      []int{http.StatusUnprocessableEntity},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			controller := gomock.NewController(t)
			store := NewMockStore(controller)
			store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				Return(nil, true, nil)

			// Exactly one of the two store calls may happen, and gomock fails
			// the row if the other one does.
			if testCase.wantReleased {
				store.EXPECT().Release(gomock.Any(), gomock.Any(), gomock.Any()).Return(true, nil)
			} else {
				store.EXPECT().Complete(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(true, nil)
			}

			var seen []int

			opts := testCase.opts
			if testCase.withFunc {
				opts = append(opts, WithClientErrorPolicyFunc(func(_ fiber.Ctx, status int) ClientErrorPolicy {
					seen = append(seen, status)

					if testCase.returns != nil {
						return *testCase.returns
					}

					// The consumer's rule: a refusal written below the guard
					// must not spend the key; everything else is cached.
					if status == http.StatusTooManyRequests || status == http.StatusPaymentRequired {
						return ClientErrorPolicyRelease
					}

					return ClientErrorPolicyCache
				}))
			}

			var calls atomic.Int64

			app := countingMoneyApp(NewWithStore(store, opts...).Check(), "tenant-client-func", &calls,
				func(c fiber.Ctx) error {
					return c.Status(testCase.handlerStatus).JSON(fiber.Map{"code": "answered"})
				})

			response := doPost(t, app, "client-func-key")
			response.Body.Close()

			assert.Equal(t, testCase.handlerStatus, response.StatusCode,
				"the policy decides what happens to the key, never what the client receives")
			assert.Equal(t, int64(1), calls.Load())
			assert.Equal(t, testCase.wantSeen, seen, "the statuses the func was consulted with")
		})
	}
}

// clientPolicyPtr and serverPolicyPtr give a table row a policy value to
// return, including one that is neither declared constant.
func clientPolicyPtr(policy ClientErrorPolicy) *ClientErrorPolicy { return &policy }

func serverPolicyPtr(policy ServerErrorPolicy) *ServerErrorPolicy { return &policy }

// consulted records one call into a policy function.
type consulted struct {
	status int
	err    error
}

// errDownstreamDeclined stands for the consumer's MTCH-0513: a downstream target
// declined the operation before anything was written, so the handler's 5xx is
// known NOT to have applied.
var errDownstreamDeclined = errors.New("downstream target declined")

// TestCheck_ServerErrorPolicyFunc_DecidesPerResponse covers the case the enum
// cannot serve: [ServerErrorPolicyFence] holds the key for every 5xx as "this
// may have been applied", but a route knows some of its failures did not apply
// and wants those keys freed while the ambiguous ones stay fenced.
//
// The two arguments carry different facts and a route uses whichever it has. A
// handler that RETURNS an error has not reached the application's Fiber error
// handler yet, so the status here is still the untouched 200 and only err
// identifies the failure; a handler that WROTE a 5xx and returned nil is the
// mirror image. The rows pin both.
func TestCheck_ServerErrorPolicyFunc_DecidesPerResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// withFunc installs the per-response seam. The rows that leave it off
		// pin that the enum path is untouched.
		withFunc bool
		opts     []Option
		// handler is the failure under test: a returned error, or a written 5xx.
		handler fiber.Handler
		// wantReleased is the observable: a released key is compare-safely
		// freed and may run again; anything else is written through Complete,
		// as a terminal fence record when wantFenced says so.
		wantReleased bool
		wantFenced   bool
		wantSeen     []consulted
		// returns, when set, is what the func answers instead of the consumer
		// rule below — the lane for a value that is neither constant.
		returns *ServerErrorPolicy
	}{
		{
			name:         "func releases a failure the route knows did not apply",
			withFunc:     true,
			handler:      func(fiber.Ctx) error { return errDownstreamDeclined },
			wantReleased: true,
			wantFenced:   false,
			wantSeen:     []consulted{{status: http.StatusOK, err: errDownstreamDeclined}},
		},
		{
			name:         "func fences a 5xx the route cannot account for",
			withFunc:     true,
			handler:      func(c fiber.Ctx) error { return c.SendStatus(http.StatusInternalServerError) },
			wantReleased: false,
			wantFenced:   true,
			wantSeen:     []consulted{{status: http.StatusInternalServerError, err: nil}},
		},
		{
			name:         "func is never consulted for a success",
			withFunc:     true,
			handler:      func(c fiber.Ctx) error { return c.SendStatus(http.StatusCreated) },
			wantReleased: false,
			wantFenced:   false,
			wantSeen:     nil,
		},
		{
			name:         "func overrides the release enum",
			withFunc:     true,
			opts:         []Option{WithServerErrorPolicy(ServerErrorPolicyRelease)},
			handler:      func(c fiber.Ctx) error { return c.SendStatus(http.StatusInternalServerError) },
			wantReleased: false,
			wantFenced:   true,
			wantSeen:     []consulted{{status: http.StatusInternalServerError, err: nil}},
		},
		{
			name:         "func overrides the fence enum",
			withFunc:     true,
			opts:         []Option{WithServerErrorPolicy(ServerErrorPolicyFence)},
			handler:      func(fiber.Ctx) error { return errDownstreamDeclined },
			wantReleased: true,
			wantFenced:   false,
			wantSeen:     []consulted{{status: http.StatusOK, err: errDownstreamDeclined}},
		},
		{
			name:         "fence enum stands when no func is set",
			opts:         []Option{WithServerErrorPolicy(ServerErrorPolicyFence)},
			handler:      func(c fiber.Ctx) error { return c.SendStatus(http.StatusInternalServerError) },
			wantReleased: false,
			wantFenced:   true,
		},
		{
			name:         "release default stands when no func is set",
			handler:      func(c fiber.Ctx) error { return c.SendStatus(http.StatusInternalServerError) },
			wantReleased: true,
			wantFenced:   false,
		},
		{
			// Neither constant, so it reads as the documented default rather
			// than as a third behaviour — and the default is the SAFE-TO-RETRY
			// one, which is why a route relying on the fence must return it
			// explicitly.
			name:         "an out-of-range return reads as the release default",
			withFunc:     true,
			opts:         []Option{WithServerErrorPolicy(ServerErrorPolicyFence)},
			handler:      func(c fiber.Ctx) error { return c.SendStatus(http.StatusInternalServerError) },
			returns:      serverPolicyPtr(ServerErrorPolicy(9)),
			wantReleased: true,
			wantFenced:   false,
			wantSeen:     []consulted{{status: http.StatusInternalServerError, err: nil}},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			controller := gomock.NewController(t)
			store := NewMockStore(controller)
			store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				Return(nil, true, nil)

			// A fence and an ordinary receipt both go through Complete and are
			// told apart by the BYTES; a release goes through Release. gomock
			// fails the row if the wrong call arrives.
			var stored []byte

			if testCase.wantReleased {
				store.EXPECT().Release(gomock.Any(), gomock.Any(), gomock.Any()).Return(true, nil)
			} else {
				store.EXPECT().Complete(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, _, written []byte, _ time.Duration) (bool, error) {
						stored = append([]byte(nil), written...)

						return true, nil
					})
			}

			var seen []consulted

			opts := testCase.opts
			if testCase.withFunc {
				opts = append(opts, WithServerErrorPolicyFunc(
					func(_ fiber.Ctx, status int, err error) ServerErrorPolicy {
						seen = append(seen, consulted{status: status, err: err})

						if testCase.returns != nil {
							return *testCase.returns
						}

						if errors.Is(err, errDownstreamDeclined) {
							return ServerErrorPolicyRelease
						}

						return ServerErrorPolicyFence
					}))
			}

			var calls atomic.Int64

			app := countingMoneyApp(NewWithStore(store, opts...).Check(), "tenant-server-func", &calls, testCase.handler)

			response := doPost(t, app, "server-func-key")
			response.Body.Close()

			assert.Equal(t, int64(1), calls.Load())
			assert.Equal(t, testCase.wantSeen, seen, "what the func was consulted with")

			switch {
			case testCase.wantFenced:
				assert.Contains(t, string(stored), `"outcome":"`+outcomeUnrecorded+`"`,
					"a fenced key holds a terminal outcome-unknown record")
			case !testCase.wantReleased:
				assert.NotContains(t, string(stored), outcomeUnrecorded,
					"an ordinary completion must not be written as a fence")
			}
		})
	}
}

// TestCheck_A4xxReturnedAsAnError_ReachesTheServerFunc pins the routing trap
// between the two seams, and what the server seam is told when it is taken.
//
// A 4xx has two shapes. WRITING the status and returning nil is a client error
// and reaches the client function. RETURNING it — fiber.NewError, which the
// application's Fiber error handler turns into a document later — has written
// no response at all, so the middleware sees a handler failure: the SERVER
// function is consulted and the client one is never called. lib-commons' own
// rate limiter produces both shapes depending on whether a WithExceededHandler
// is installed, so this is the routing a rate-limited route actually meets.
//
// The status the server seam receives is the EFFECTIVE one: the code carried by
// a returned *fiber.Error, the written status when the handler wrote anything,
// and otherwise the untouched 200. err is the only fact among them, which is
// why every row asserts it too.
func TestCheck_A4xxReturnedAsAnError_ReachesTheServerFunc(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		handler    fiber.Handler
		wantStatus int
		// wantFiberCode is the code inside err when err is a *fiber.Error, and
		// zero when the row's error is a plain one.
		wantFiberCode int
	}{
		{
			name:          "a returned fiber error reports its own code",
			handler:       func(fiber.Ctx) error { return fiber.NewError(http.StatusBadRequest, "rejected") },
			wantStatus:    http.StatusBadRequest,
			wantFiberCode: http.StatusBadRequest,
		},
		{
			// Nothing to forecast from: the response is untouched and the error
			// carries no status, so the seam is told exactly that.
			name:       "a returned plain error leaves the untouched status",
			handler:    func(fiber.Ctx) error { return errDownstreamDeclined },
			wantStatus: http.StatusOK,
		},
		{
			// A written response is a fact and outranks the forecast.
			name: "a written status outranks the code inside the error",
			handler: func(c fiber.Ctx) error {
				if err := c.Status(http.StatusUnprocessableEntity).JSON(fiber.Map{"code": "INVALID"}); err != nil {
					return err
				}

				return fiber.NewError(http.StatusBadRequest, "rejected")
			},
			wantStatus:    http.StatusUnprocessableEntity,
			wantFiberCode: http.StatusBadRequest,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			controller := gomock.NewController(t)
			store := NewMockStore(controller)
			store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				Return(nil, true, nil)
			// Both funcs return their default policy, which releases.
			store.EXPECT().Release(gomock.Any(), gomock.Any(), gomock.Any()).Return(true, nil)

			var (
				clientSeen []int
				serverSeen []consulted
			)

			middleware := NewWithStore(store,
				WithClientErrorPolicyFunc(func(_ fiber.Ctx, status int) ClientErrorPolicy {
					clientSeen = append(clientSeen, status)

					return ClientErrorPolicyRelease
				}),
				WithServerErrorPolicyFunc(func(_ fiber.Ctx, status int, err error) ServerErrorPolicy {
					serverSeen = append(serverSeen, consulted{status: status, err: err})

					return ServerErrorPolicyRelease
				}),
			)

			var calls atomic.Int64

			app := countingMoneyApp(middleware.Check(), "tenant-returned-4xx", &calls, testCase.handler)

			response := doPost(t, app, "returned-4xx-key")
			response.Body.Close()

			assert.Equal(t, int64(1), calls.Load())
			assert.Nil(t, clientSeen, "a failure delivered as an error is never a client error here")
			require.Len(t, serverSeen, 1, "it reaches the server seam instead")
			assert.Equal(t, testCase.wantStatus, serverSeen[0].status)
			require.Error(t, serverSeen[0].err, "err is the fact the seam can rely on")

			var fiberErr *fiber.Error

			if testCase.wantFiberCode == 0 {
				assert.NotErrorAs(t, serverSeen[0].err, &fiberErr)

				return
			}

			require.ErrorAs(t, serverSeen[0].err, &fiberErr)
			assert.Equal(t, testCase.wantFiberCode, fiberErr.Code)
		})
	}
}
