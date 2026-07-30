//go:build unit

package problem

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBaseURI(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "https://errors.lerian.studio/v1", BaseURI)
}

// TestDetail_SatisfiesStatusError proves *Detail implements huma.StatusError via
// method promotion from the embedded ErrorModel — the property that lets the
// override and MapError return a *Detail wherever Huma expects a StatusError.
func TestDetail_SatisfiesStatusError(t *testing.T) {
	t.Parallel()

	var _ huma.StatusError = (*Detail)(nil)

	d := &Detail{
		ErrorModel: huma.ErrorModel{Status: http.StatusTeapot, Title: "I'm a teapot", Detail: "nope"},
	}

	var se huma.StatusError = d
	assert.Equal(t, http.StatusTeapot, se.GetStatus())
	assert.NotEmpty(t, se.Error())
}

// TestDetail_JSON_OmitsEmptyCode asserts the omitempty contract: a code-less
// Detail must not emit a `code` key on the wire, so code-less rails keep a bare
// RFC 9457 body.
func TestDetail_JSON_OmitsEmptyCode(t *testing.T) {
	t.Parallel()

	d := &Detail{
		ErrorModel: huma.ErrorModel{
			Status: http.StatusBadRequest,
			Title:  http.StatusText(http.StatusBadRequest),
			Detail: "bad input",
		},
	}

	raw, err := json.Marshal(d)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))

	_, hasCode := m["code"]
	assert.False(t, hasCode, "empty Code must be dropped by omitempty, got %s", raw)
	assert.Equal(t, "bad input", m["detail"])
	assert.EqualValues(t, http.StatusBadRequest, m["status"])
}

// TestDetail_JSON_IncludesCode asserts that once Code is set it serializes under
// the `code` key.
func TestDetail_JSON_IncludesCode(t *testing.T) {
	t.Parallel()

	d := &Detail{
		ErrorModel: huma.ErrorModel{
			Status: http.StatusUnprocessableEntity,
			Title:  http.StatusText(http.StatusUnprocessableEntity),
			Detail: "boom",
			Type:   BaseURI + "/SPB-3002",
		},
		Code: "SPB-3002",
	}

	raw, err := json.Marshal(d)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))

	assert.Equal(t, "SPB-3002", m["code"])
	assert.Equal(t, BaseURI+"/SPB-3002", m["type"])
}

// TestDetail_JSON_OmitsAbsentUpstream asserts a problem document with no
// upstream member keeps the exact wire shape it has today — the additive
// guarantee for every consumer that never proxies a third party.
func TestDetail_JSON_OmitsAbsentUpstream(t *testing.T) {
	t.Parallel()

	d := &Detail{
		ErrorModel: huma.ErrorModel{
			Status: http.StatusBadRequest,
			Title:  http.StatusText(http.StatusBadRequest),
			Detail: "bad input",
		},
	}

	raw, err := json.Marshal(d)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))

	_, hasUpstream := m["upstream"]
	assert.False(t, hasUpstream, "absent Upstream must be dropped by omitempty, got %s", raw)
}

// TestDetail_JSON_IncludesUpstream asserts the extension member serializes as a
// top-level `upstream` object with the provider's own code and message — the
// two fields a client automates against.
func TestDetail_JSON_IncludesUpstream(t *testing.T) {
	t.Parallel()

	d := &Detail{
		ErrorModel: huma.ErrorModel{
			Status: http.StatusBadGateway,
			Title:  http.StatusText(http.StatusBadGateway),
			Detail: "internal error",
		},
		Upstream: &Upstream{Code: "E4001", Message: "CPF não encontrado na base"},
	}

	raw, err := json.Marshal(d)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))

	up, ok := m["upstream"].(map[string]any)
	require.True(t, ok, "upstream must be a top-level RFC 9457 extension object, got %s", raw)
	assert.Equal(t, "E4001", up["code"])
	assert.Equal(t, "CPF não encontrado na base", up["message"])
}

// TestUpstream_JSON_BoundsEachField proves the member cannot become a raw
// response-body dump: both fields are bounded on the wire, whatever the
// construction path, and the truncation is rune-safe so multi-byte text stays
// valid UTF-8.
func TestUpstream_JSON_BoundsEachField(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é", maxUpstreamMessageLen+50)
	d := &Detail{Upstream: &Upstream{
		Code:    strings.Repeat("C", maxUpstreamCodeLen+10),
		Message: long,
	}}

	raw, err := json.Marshal(d)
	require.NoError(t, err)
	require.True(t, json.Valid(raw))

	var got struct {
		Upstream Upstream `json:"upstream"`
	}
	require.NoError(t, json.Unmarshal(raw, &got))

	assert.Equal(t, maxUpstreamCodeLen, len([]rune(strings.TrimSuffix(got.Upstream.Code, truncationMark))))
	assert.True(t, strings.HasSuffix(got.Upstream.Code, truncationMark), "truncation must be visible")
	assert.Equal(t, maxUpstreamMessageLen, len([]rune(strings.TrimSuffix(got.Upstream.Message, truncationMark))))
	assert.True(t, strings.HasSuffix(got.Upstream.Message, truncationMark))
	assert.True(t, utf8.ValidString(got.Upstream.Message), "rune-safe truncation must keep valid UTF-8")
}

// TestUpstream_JSON_KeepsMultiByteUnderTheBound proves the bound counts
// characters, not bytes: an accented provider message that fits the bound
// arrives whole (Portuguese rail messages are the common case).
func TestUpstream_JSON_KeepsMultiByteUnderTheBound(t *testing.T) {
	t.Parallel()

	msg := strings.Repeat("é", maxUpstreamMessageLen-1) // twice as many bytes as runes
	raw, err := json.Marshal(&Upstream{Code: "E1", Message: msg})
	require.NoError(t, err)

	var got Upstream
	require.NoError(t, json.Unmarshal(raw, &got))

	assert.Equal(t, msg, got.Message, "a message under the bound must not be truncated")
	assert.NotContains(t, got.Message, truncationMark)
}

// TestUpstream_JSON_OmitsEmptyFields asserts a provider that reports only a
// message does not put an empty `code` on the wire.
func TestUpstream_JSON_OmitsEmptyFields(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(&Upstream{Message: "rail unavailable"})
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))

	_, hasCode := m["code"]
	assert.False(t, hasCode, "empty upstream code must be dropped, got %s", raw)
	assert.Equal(t, "rail unavailable", m["message"])
}

// TestUpstream_Error proves *Upstream is usable as an error argument (that is
// how it reaches huma.NewError) and that a nil receiver does not panic — the
// house zero-panic rule on a value that crosses a package boundary.
func TestUpstream_Error(t *testing.T) {
	t.Parallel()

	var asErr error = &Upstream{Code: "E1", Message: "boom"}
	assert.Contains(t, asErr.Error(), "E1")
	assert.Contains(t, asErr.Error(), "boom")

	assert.Equal(t, "just a message", (&Upstream{Message: "just a message"}).Error())
	assert.Contains(t, (&Upstream{Code: "E9"}).Error(), "E9")

	assert.NotPanics(t, func() {
		var nilUp *Upstream
		assert.Empty(t, nilUp.Error())

		raw, err := nilUp.MarshalJSON()
		require.NoError(t, err)
		assert.JSONEq(t, "null", string(raw))
	})
}
