//go:build unit

package problem

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDetail_JSON_InlinesExtensions pins the wire shape byte for byte: the
// standard members first, in declaration order, then the extension members as
// top-level keys in sorted order, so the same body always renders the same bytes.
func TestDetail_JSON_InlinesExtensions(t *testing.T) {
	t.Parallel()

	d := &Detail{
		ErrorModel: huma.ErrorModel{Status: http.StatusInternalServerError, Title: "Internal Server Error", Detail: "internal error"},
		Code:       "BTF-9011",
		Extensions: Extensions{"transferId": "770e8400", "openTime": "06:30"},
	}

	raw, err := json.Marshal(d)
	require.NoError(t, err)
	assert.Equal(t, `{"title":"Internal Server Error","status":500,"detail":"internal error","code":"BTF-9011","openTime":"06:30","transferId":"770e8400"}`, string(raw))

	value, err := json.Marshal(*d)
	require.NoError(t, err)
	assert.Equal(t, string(raw), string(value), "a Detail value and a *Detail must render the same body")
}

// TestDetail_JSON_ExtensionsCannotShadowTheDocumentsOwnMembers proves an
// extension can only ADD a member: a key naming a standard or library member is
// dropped, so no call site can rewrite the status, the code or the scrubbed
// detail through the extension map.
func TestDetail_JSON_ExtensionsCannotShadowTheDocumentsOwnMembers(t *testing.T) {
	t.Parallel()

	d := &Detail{
		ErrorModel: huma.ErrorModel{Status: http.StatusServiceUnavailable, Title: "Service Unavailable", Detail: "internal error"},
		Code:       "BTF-0019",
		Extensions: Extensions{
			"type": "x", "title": "x", "status": 200, "detail": "raw cause", "instance": "x",
			"errors": "x", "code": "OTHER", "upstream": "x", "refusalCode": "IDEMPOTENCY_UNFENCED",
		},
	}

	raw, err := json.Marshal(d)
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))

	assert.Equal(t, map[string]any{
		"title": "Service Unavailable", "status": float64(http.StatusServiceUnavailable),
		"detail": "internal error", "code": "BTF-0019", "refusalCode": "IDEMPOTENCY_UNFENCED",
	}, body)
}

// TestDetail_JSON_NoExtensionsLeavesTheBodyUnchanged is the additive guarantee
// for every service that never sets one: its bodies render exactly as before.
func TestDetail_JSON_NoExtensionsLeavesTheBodyUnchanged(t *testing.T) {
	t.Parallel()

	for name, ext := range map[string]Extensions{"nil": nil, "empty": {}, "only reserved keys": {"status": 1}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d := &Detail{ErrorModel: huma.ErrorModel{Status: http.StatusNotFound, Title: "Not Found"}, Extensions: ext}

			raw, err := json.Marshal(d)
			require.NoError(t, err)
			assert.Equal(t, `{"title":"Not Found","status":404}`, string(raw))
		})
	}
}

// TestDetail_Schema_AllowsAdditionalProperties keeps the published contract
// honest: a body can carry extension members, so the generated error schema must
// not declare additionalProperties false, which a strict client validator would
// enforce against every body that carries one.
func TestDetail_Schema_AllowsAdditionalProperties(t *testing.T) {
	t.Parallel()

	registry := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
	registry.Schema(reflect.TypeFor[Detail](), true, "")

	schema := registry.Map()["Detail"]
	require.NotNil(t, schema)
	assert.Equal(t, true, schema.AdditionalProperties)
	assert.NotContains(t, schema.Properties, "extensions", "the map is inlined, never published as its own member")
}

// TestMapError_PublicDetail_ReachesTheWireOnA5xx is the case the type exists for:
// a coded 5xx whose remedy the client must read ("do not resend") keeps it, while
// the codeOf message, which may carry a raw cause, stays scrubbed.
func TestMapError_PublicDetail_ReachesTheWireOnA5xx(t *testing.T) {
	t.Parallel()

	domainErr := fmt.Errorf("process transfer: %w", errors.Join(
		errors.New("redis: connection reset"),
		PublicDetail("the key protects nothing; do not resend this request"),
	))
	codeOf := func(error) (string, string, bool) { return "BTF-0019", "redis: connection reset", true }
	statusOf := func(string) int { return http.StatusServiceUnavailable }

	d := mapErrDetail(t, MapError(domainErr, codeOf, statusOf, "BTF-9000"))

	assert.Equal(t, http.StatusServiceUnavailable, d.Status)
	assert.Equal(t, "the key protects nothing; do not resend this request", d.Detail)
	assert.Equal(t, "BTF-0019", d.Code)
}

// TestMapError_Extensions_ReachTheWireAtEveryStatus proves the members survive
// the 5xx scrub (the stranded-hold 500 carries the transfer the client must
// poll) and ride a 4xx unchanged, through a wrapped chain in both cases.
func TestMapError_Extensions_ReachTheWireAtEveryStatus(t *testing.T) {
	t.Parallel()

	for name, status := range map[string]int{"5xx": http.StatusInternalServerError, "4xx": http.StatusConflict} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			domainErr := fmt.Errorf("initiate: %w", errors.Join(errors.New("stranded"), Extensions{"transferId": "770e8400"}))
			codeOf := func(error) (string, string, bool) { return "BTF-9011", "stranded", true }

			d := mapErrDetail(t, MapError(domainErr, codeOf, func(string) int { return status }, "BTF-9000"))

			assert.Equal(t, status, d.Status)
			assert.Equal(t, Extensions{"transferId": "770e8400"}, d.Extensions)
		})
	}
}

// TestMapError_EmptyMembersAreAbsent pins the degenerate inputs: an empty
// PublicDetail does not blank the scrubbed detail and an empty map publishes
// nothing, exactly like an empty *Upstream.
func TestMapError_EmptyMembersAreAbsent(t *testing.T) {
	t.Parallel()

	domainErr := errors.Join(errors.New("x"), PublicDetail(""), Extensions{})
	codeOf := func(error) (string, string, bool) { return "BTF-9000", "x", true }

	d := mapErrDetail(t, MapError(domainErr, codeOf, func(string) int { return http.StatusInternalServerError }, "BTF-9000"))

	assert.Equal(t, genericServerErrorDetail, d.Detail)
	assert.Nil(t, d.Extensions)
}

// TestNewError_ServerError_KeepsCuratedMembersAndScrubsTheRest is the Install
// seam's half: on a 5xx the curated detail and extensions survive, while the raw
// msg and every other err stay out of the body.
func TestNewError_ServerError_KeepsCuratedMembersAndScrubsTheRest(t *testing.T) {
	t.Parallel()

	d := asDetail(t, newError(
		http.StatusServiceUnavailable,
		"db password = hunter2",
		errors.New("leaky raw cause"),
		PublicDetail("retry after the seconds on Retry-After"),
		Extensions{"refusalCode": "IDEMPOTENCY_CONFLICT"},
	))

	assert.Equal(t, "retry after the seconds on Retry-After", d.Detail)
	assert.Nil(t, d.Errors, "5xx must still fold NO errs")
	assert.Equal(t, Extensions{"refusalCode": "IDEMPOTENCY_CONFLICT"}, d.Extensions)
}

// TestNewError_ClientError_CuratedMembersAreNeverFolded proves the members land
// in exactly one place on the wire: never duplicated into errors[], while the
// unrelated errs keep folding in order.
func TestNewError_ClientError_CuratedMembersAreNeverFolded(t *testing.T) {
	t.Parallel()

	d := asDetail(t, newError(
		http.StatusBadRequest,
		"invalid input",
		errors.New("field a invalid"),
		Extensions{"openTime": "06:30"},
		PublicDetail("the window opens at 06:30"),
	))

	assert.Equal(t, "the window opens at 06:30", d.Detail)
	require.Len(t, d.Errors, 1)
	assert.Equal(t, "field a invalid", d.Errors[0].Message)
	assert.Equal(t, Extensions{"openTime": "06:30"}, d.Extensions)
}
