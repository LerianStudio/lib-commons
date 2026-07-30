//go:build unit

package problem

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// detailErr is a test error implementing huma.ErrorDetailer to prove newError
// honors the interface (folding the carried *huma.ErrorDetail rather than the
// Error() string) on the <500 path.
type detailErr struct {
	d *huma.ErrorDetail
}

func (e *detailErr) Error() string                  { return e.d.Message }
func (e *detailErr) ErrorDetail() *huma.ErrorDetail { return e.d }

// asDetail asserts the StatusError is a *Detail and returns it.
func asDetail(t *testing.T, se huma.StatusError) *Detail {
	t.Helper()

	d, ok := se.(*Detail)
	require.True(t, ok, "expected *Detail, got %T", se)

	return d
}

// TestNewError_ServerError_Scrubbed is the central safety assertion: every
// status>=500 is scrubbed to the static detail and folds NO errs, even when a
// raw cause is passed (the leak underwriter's scrubber closes).
func TestNewError_ServerError_Scrubbed(t *testing.T) {
	t.Parallel()

	for _, status := range []int{
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
	} {
		d := asDetail(t, newError(status, "db password = hunter2", errors.New("leaky raw cause")))

		assert.Equal(t, status, d.Status)
		assert.Equal(t, http.StatusText(status), d.Title)
		assert.Equal(t, genericServerErrorDetail, d.Detail, "5xx detail must be scrubbed")
		assert.Nil(t, d.Errors, "5xx must fold NO errs")
		assert.Empty(t, d.Code)
		assert.Empty(t, d.Type, "Type stays RFC default about:blank (empty)")
		assert.Nil(t, d.Upstream, "an unrelated 5xx cause must NOT surface as an upstream member")
	}
}

// TestNewError_ClientError_PassthroughAndFold proves the <500 path: msg passes
// through and errs fold into Errors[] in order — preserving Huma's native 422
// field-error behavior.
func TestNewError_ClientError_PassthroughAndFold(t *testing.T) {
	t.Parallel()

	d := asDetail(t, newError(
		http.StatusUnprocessableEntity,
		"validation failed",
		errors.New("field a invalid"),
		errors.New("field b invalid"),
	))

	assert.Equal(t, http.StatusUnprocessableEntity, d.Status)
	assert.Equal(t, "validation failed", d.Detail)
	require.Len(t, d.Errors, 2)
	assert.Equal(t, "field a invalid", d.Errors[0].Message)
	assert.Equal(t, "field b invalid", d.Errors[1].Message, "order preserved")
}

// TestNewError_ClientError_SkipsNilErrs proves nil entries are skipped and an
// all-nil / empty slice yields a nil Errors (not an empty []).
func TestNewError_ClientError_SkipsNilErrs(t *testing.T) {
	t.Parallel()

	t.Run("nil interspersed", func(t *testing.T) {
		t.Parallel()

		d := asDetail(t, newError(http.StatusBadRequest, "msg", nil, errors.New("real"), nil))

		require.Len(t, d.Errors, 1)
		assert.Equal(t, "real", d.Errors[0].Message)
	})

	t.Run("no errs at all", func(t *testing.T) {
		t.Parallel()

		d := asDetail(t, newError(http.StatusBadRequest, "msg"))
		assert.Nil(t, d.Errors, "empty slice must become nil, not []")
	})

	t.Run("all nil", func(t *testing.T) {
		t.Parallel()

		d := asDetail(t, newError(http.StatusNotFound, "missing", nil, nil))
		assert.Nil(t, d.Errors)
	})
}

// TestNewError_HonorsErrorDetailer proves a huma.ErrorDetailer's carried detail
// is folded (not its Error() string) on the <500 path.
func TestNewError_HonorsErrorDetailer(t *testing.T) {
	t.Parallel()

	carried := &huma.ErrorDetail{Message: "carried", Location: "body.name", Value: "x"}
	d := asDetail(t, newError(http.StatusBadRequest, "msg", &detailErr{d: carried}))

	require.Len(t, d.Errors, 1)
	assert.Same(t, carried, d.Errors[0], "ErrorDetailer's *ErrorDetail folded verbatim")
	assert.Equal(t, "body.name", d.Errors[0].Location)
}

// nilDetailErr is a test error implementing huma.ErrorDetailer whose
// ErrorDetail() returns a nil *huma.ErrorDetail, exercising the skip-nil guard
// on the <500 fold path.
type nilDetailErr struct{}

func (e *nilDetailErr) Error() string                  { return "nil detail" }
func (e *nilDetailErr) ErrorDetail() *huma.ErrorDetail { return nil }

// TestNewError_SkipsNilErrorDetail proves an ErrorDetailer returning a nil
// *huma.ErrorDetail is skipped (no null entry in Errors[]) while a valid sibling
// is still folded.
func TestNewError_SkipsNilErrorDetail(t *testing.T) {
	t.Parallel()

	t.Run("nil detail interspersed with a valid sibling", func(t *testing.T) {
		t.Parallel()

		carried := &huma.ErrorDetail{Message: "kept"}
		d := asDetail(t, newError(
			http.StatusBadRequest,
			"msg",
			&nilDetailErr{},
			&detailErr{d: carried},
		))

		require.Len(t, d.Errors, 1, "nil ErrorDetail must be skipped, valid sibling kept")
		for _, e := range d.Errors {
			assert.NotNil(t, e, "no null entry must be folded into Errors[]")
		}
		assert.Same(t, carried, d.Errors[0])
	})

	t.Run("only a nil-detail error yields nil Errors", func(t *testing.T) {
		t.Parallel()

		d := asDetail(t, newError(http.StatusBadRequest, "msg", &nilDetailErr{}))
		assert.Nil(t, d.Errors, "a sole nil ErrorDetail leaves Errors nil, not [null]")
	})
}

// TestInstall_OverridesGlobal_Idempotent proves Install swaps the process-global
// huma.NewError to our *Detail-producing override and that a second call is a
// safe no-op. It restores the global afterward to avoid leaking across packages.
func TestInstall_OverridesGlobal_Idempotent(t *testing.T) {
	// NOT parallel: mutates the process-global huma.NewError.
	original := huma.NewError
	t.Cleanup(func() { huma.NewError = original })

	Install()
	first := asDetail(t, huma.NewError(http.StatusBadRequest, "after install"))
	assert.Equal(t, "after install", first.Detail)

	// A second Install must not re-wrap or otherwise change behavior.
	Install()
	scrubbed := asDetail(t, huma.NewError(http.StatusInternalServerError, "raw cause"))
	assert.Equal(t, genericServerErrorDetail, scrubbed.Detail)
}

// TestInstall_ReinstallsAfterRestore is the anti-Once lock. The override is a
// process-global that anything in a test binary can put back; if Install skipped
// work after the first call, the next one would be a SILENT no-op and every
// later 5xx-scrub assertion in that binary would be measuring stock Huma while
// appearing to measure ours. Install must republish, every time.
func TestInstall_ReinstallsAfterRestore(t *testing.T) {
	// NOT parallel: mutates the process-global huma.NewError.
	original := huma.NewError
	t.Cleanup(func() { huma.NewError = original })

	Install()
	huma.NewError = original // exactly what a sibling test's cleanup does

	Install()

	scrubbed := asDetail(t, huma.NewError(http.StatusInternalServerError, "raw cause"))
	assert.Equal(t, genericServerErrorDetail, scrubbed.Detail,
		"a reinstall after a restore must take effect, not be skipped")
}

// TestNewError_ServerError_PreservesUpstream is the other half of the scrub
// contract: a curated *Upstream survives a status>=500 while everything else
// about the body stays scrubbed. Without this, a client proxying a third party
// gets an opaque 5xx exactly where the provider's own code is the only
// diagnosable information it has.
func TestNewError_ServerError_PreservesUpstream(t *testing.T) {
	t.Parallel()

	d := asDetail(t, newError(
		http.StatusBadGateway,
		"db password = hunter2",
		&Upstream{Code: "E4001", Message: "CPF não encontrado na base"},
		errors.New("leaky raw cause"),
	))

	assert.Equal(t, genericServerErrorDetail, d.Detail, "5xx detail must still be scrubbed")
	assert.Nil(t, d.Errors, "5xx must still fold NO errs")
	require.NotNil(t, d.Upstream, "the curated upstream member must survive the 5xx scrub")
	assert.Equal(t, "E4001", d.Upstream.Code)
	assert.Equal(t, "CPF não encontrado na base", d.Upstream.Message)
}

// TestNewError_UpstreamNotFoldedIntoErrors proves the member is the ONLY place
// upstream data lands: it is never duplicated into errors[], and unrelated errs
// keep folding exactly as before on the <500 path.
func TestNewError_UpstreamNotFoldedIntoErrors(t *testing.T) {
	t.Parallel()

	d := asDetail(t, newError(
		http.StatusUnprocessableEntity,
		"rail refused the proposal",
		errors.New("field a invalid"),
		&Upstream{Code: "E22", Message: "prazo inválido"},
		errors.New("field b invalid"),
	))

	assert.Equal(t, "rail refused the proposal", d.Detail, "<500 msg still passes through")
	require.Len(t, d.Errors, 2, "only the unrelated errs fold, in order")
	assert.Equal(t, "field a invalid", d.Errors[0].Message)
	assert.Equal(t, "field b invalid", d.Errors[1].Message)
	require.NotNil(t, d.Upstream)
	assert.Equal(t, "E22", d.Upstream.Code)
}

// TestNewError_UpstreamThroughWrappedError proves detection unwraps: real call
// sites wrap the rail error with local context before returning it.
func TestNewError_UpstreamThroughWrappedError(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("call rail: %w", &Upstream{Code: "E7", Message: "indisponível"})
	d := asDetail(t, newError(http.StatusInternalServerError, "boom", wrapped))

	require.NotNil(t, d.Upstream, "a wrapped upstream must still be found")
	assert.Equal(t, "E7", d.Upstream.Code)
	assert.Equal(t, genericServerErrorDetail, d.Detail)
}

// TestNewError_UpstreamEdgeCases pins the degenerate inputs: the first match
// wins, an empty member is treated as absent, and a typed-nil *Upstream neither
// attaches a member nor panics.
func TestNewError_UpstreamEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("first match wins", func(t *testing.T) {
		t.Parallel()

		d := asDetail(t, newError(
			http.StatusBadGateway, "boom",
			&Upstream{Code: "first"},
			&Upstream{Code: "second"},
		))

		require.NotNil(t, d.Upstream)
		assert.Equal(t, "first", d.Upstream.Code)
	})

	t.Run("empty member is absent", func(t *testing.T) {
		t.Parallel()

		d := asDetail(t, newError(http.StatusBadGateway, "boom", &Upstream{}))
		assert.Nil(t, d.Upstream, "a member with neither code nor message must not reach the wire")
	})

	t.Run("typed-nil upstream", func(t *testing.T) {
		t.Parallel()

		assert.NotPanics(t, func() {
			var up *Upstream
			d := asDetail(t, newError(http.StatusBadRequest, "boom", up))
			assert.Nil(t, d.Upstream)
			assert.Nil(t, d.Errors, "a typed-nil upstream must not fold a bogus errors[] entry")
		})
	})
}

// installForTest publishes the override on the process-global huma.NewError for
// the duration of t and restores the previous value afterward. It drives the real
// Install(), which is safe here only because Install is re-entrant: were it
// guarded by a sync.Once, an earlier test that installed and then restored the
// global would make this call a silent no-op and every assertion below would be
// measuring stock Huma.
func installForTest(t *testing.T) {
	t.Helper()

	original := huma.NewError
	t.Cleanup(func() { huma.NewError = original })

	Install()
}

// TestInstall_ServerError_UpstreamReachesTheWire is the end-to-end proof through
// the installed process-global override and JSON encoding — the same path a
// Huma handler takes when a call site returns huma.Error502BadGateway. It
// asserts the client-visible 5xx body: provider code and message present,
// everything of ours scrubbed.
func TestInstall_ServerError_UpstreamReachesTheWire(t *testing.T) {
	// NOT parallel: mutates the process-global huma.NewError.
	installForTest(t)

	se := huma.Error502BadGateway("rail refused", &Upstream{Code: "E4001", Message: "CPF não encontrado na base"})

	raw, err := json.Marshal(se)
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))

	assert.Equal(t, genericServerErrorDetail, body["detail"], "our own 5xx detail stays scrubbed")
	_, hasErrors := body["errors"]
	assert.False(t, hasErrors, "no errors[] on a 5xx")

	up, ok := body["upstream"].(map[string]any)
	require.True(t, ok, "upstream member must reach the wire on a 5xx, got %s", raw)
	assert.Equal(t, "E4001", up["code"])
	assert.Equal(t, "CPF não encontrado na base", up["message"])
}
