//go:build unit

package idempotency

import (
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReplayUnavailable_BypassesTheOtherRefusalSeams fences the refusal off from
// the two seams it must never fall through to.
//
// Both of them exist to say something this case must not say.
// [WithPostHandlerUnavailableHandler] answers a receipt that failed for an
// unknown reason: "the mutation may or may not have happened, reconcile it".
// [WithTerminalRefusalHandler] owns the three refusals for a record this version
// cannot act on. Here the record is perfectly readable and the outcome is known
// — the operation succeeded and its response was simply too large to keep — so
// routing it through either seam would hand a service that wired one of them a
// reconciliation instruction for an operation that is already settled.
//
// The seams are therefore wired to fail the test if they are reached at all, and
// the assertion is the built-in 409 document.
func TestReplayUnavailable_BypassesTheOtherRefusalSeams(t *testing.T) {
	t.Parallel()

	app, calls := newOversizeApp(t, "tenant-replay-unavailable-seams",
		// t.Errorf, not t.Fatal: these run on the handler's goroutine, where
		// Fatal would stop the wrong one and the test would hang or pass.
		WithPostHandlerUnavailableHandler(func(c fiber.Ctx) error {
			t.Errorf("post-handler seam answered a KNOWN success; its instruction is reconcile")

			return c.SendStatus(http.StatusServiceUnavailable)
		}),
		WithTerminalRefusalHandler(func(c fiber.Ctx, code string) error {
			t.Errorf("terminal-refusal seam answered %q; it owns the three refusals, not this one", code)

			return c.SendStatus(http.StatusUnprocessableEntity)
		}),
	)

	first := postBodyWithKey(t, app, "oversize-key", `{"amount":"1250.00"}`)
	require.Equal(t, http.StatusCreated, first.StatusCode)
	require.NoError(t, first.Body.Close())

	second := postBodyWithKey(t, app, "oversize-key", `{"amount":"1250.00"}`)
	body := readBody(t, second)

	assert.Equal(t, int32(1), calls.Load())
	assert.Equal(t, http.StatusConflict, second.StatusCode)
	assert.Contains(t, body, RefusalCodeReplayUnavailable)
	assert.Contains(t, body, "already completed successfully",
		"the built-in document reports the known success, which is the whole reason it is not one of the other two")
}
