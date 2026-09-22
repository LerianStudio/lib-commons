//go:build unit

package idempotency

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
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
