//go:build unit || integration

// Additional tenant-scoped handler tests — horizontal split from
// admin_test.go once that file passed 1,600 LOC.
//
// Task 6 landed 14 tenant handler tests in admin_test.go covering happy
// paths + default-deny + invalid-tenantID + unknown-key + redaction. This
// file adds the validator-rejection matrix, body-shape edge cases, URL
// encoding, and authorizer-action propagation checks that flesh out the
// PRD AC14 surface.
//
// The shared fixtures (fakeStore, buildClient, buildTenantClientStarted,
// allowAll, allowAllTenant, tenantValueResp, tenantListResp, doRequest,
// readJSON, buildApp) live in admin_test.go — a single test binary is
// compiled for the whole package so all helpers are visible here without
// re-declaration.
package admin_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v2"

	commonshttp "github.com/LerianStudio/lib-commons/v5/commons/net/http"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/admin"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
)

// ---------------------------------------------------------------------------
// Validator rejection at the write boundary.
// ---------------------------------------------------------------------------

// TestPutTenant_ValidatorRejects400 verifies that when a tenant-scoped key
// carries a validator, a PUT that submits a rejected value surfaces 400 with
// the validation_error title. Complements Task 6's TestPut_ValidationFailure
// which exercised the same path on the legacy global route.
func TestPutTenant_ValidatorRejects400(t *testing.T) {
	t.Parallel()

	// Validator accepts 0 (the default) but rejects any negative number.
	nonNegative := func(v any) error {
		f, ok := v.(float64)
		if !ok {
			return errors.New("not a float64")
		}
		if f < 0 {
			return errors.New("must be non-negative")
		}
		return nil
	}

	c, _ := buildTenantClientStarted(t, 0.0, systemplane.WithValidator(nonNegative))

	app := buildApp(t, c, allowAll(), allowAllTenant())

	// Positive value succeeds — sanity check that the validator does not
	// reject every input.
	okResp := doRequest(t, app, http.MethodPut, "/system/global/fee.rate/tenants/tenant-A", `{"value":0.5}`)
	okResp.Body.Close()

	if okResp.StatusCode != fiber.StatusOK {
		t.Fatalf("sanity PUT: expected 200, got %d", okResp.StatusCode)
	}

	// Negative value is rejected by the validator.
	resp := doRequest(t, app, http.MethodPut, "/system/global/fee.rate/tenants/tenant-A", `{"value":-0.5}`)
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusBadRequest {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 for validator rejection, got %d: %s", resp.StatusCode, string(data))
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	if body.Title != "validation_error" {
		t.Fatalf("expected title 'validation_error', got %q", body.Title)
	}
}

// ---------------------------------------------------------------------------
// Body shape edge cases.
// ---------------------------------------------------------------------------

// TestPutTenant_InvalidJSONBodyReturns400 verifies that a syntactically
// invalid JSON body surfaces 400 BEFORE touching the Client. Fiber's
// BodyParser returns an error; handlePutTenant maps that to the generic
// "invalid request body" 400.
func TestPutTenant_InvalidJSONBodyReturns400(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, 0.0)

	app := buildApp(t, c, allowAll(), allowAllTenant())

	resp := doRequest(t, app, http.MethodPut, "/system/global/fee.rate/tenants/tenant-A", `{this is not valid JSON`)
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusBadRequest {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 for malformed JSON, got %d: %s", resp.StatusCode, string(data))
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	// The handler distinguishes "bad body" (JSON parse failure) from
	// "missing value" (parse succeeded but value field absent). Both use
	// the "bad_request" title but different messages.
	if body.Title != "bad_request" {
		t.Fatalf("expected title 'bad_request', got %q", body.Title)
	}

	if !strings.Contains(body.Message, "invalid request body") {
		t.Fatalf("expected message to mention 'invalid request body', got %q", body.Message)
	}
}

// TestPutTenant_EmptyBodyObjectReturns400 verifies that a valid JSON body
// missing the "value" field surfaces 400 with the "missing value field"
// message. This is distinct from an invalid-JSON-body error; the handler
// parses the body successfully but finds body.Value == nil.
//
// Note: the handler's existing TestPut_MissingValueField covers the legacy
// global route; this test mirrors it on the tenant route. The test name
// suggests "missing value field" rather than "empty body" because Fiber
// parses {} as valid JSON with all fields zero-valued.
func TestPutTenant_EmptyBodyObjectReturns400(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, 0.0)

	app := buildApp(t, c, allowAll(), allowAllTenant())

	// Body {} parses fine, but body.Value (json.RawMessage) stays nil. The
	// handler explicitly rejects this case with "missing value field".
	resp := doRequest(t, app, http.MethodPut, "/system/global/fee.rate/tenants/tenant-A", `{}`)
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusBadRequest {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 for missing value field, got %d: %s", resp.StatusCode, string(data))
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	if body.Title != "bad_request" {
		t.Fatalf("expected title 'bad_request', got %q", body.Title)
	}

	if !strings.Contains(body.Message, "missing value field") {
		t.Fatalf("expected 'missing value field' message, got %q", body.Message)
	}
}

// TestPutTenant_ExplicitNullValueReturns200 pins the observed behavior of
// the PUT handler when the caller submits {"value": null}.
//
// Implementation detail: body.Value is a json.RawMessage; for {"value":null}
// it holds the 4 bytes "null" (non-nil), so the "missing value field"
// branch is NOT taken. json.Unmarshal("null", &value) produces a nil any,
// which Client.SetForTenant accepts. The row is persisted with the JSON
// bytes "null" and subsequent GetForTenant returns a nil any value.
//
// Consumers who want "send null to delete" semantics MUST use the DELETE
// verb explicitly. This test exists to lock the shipped behavior so a
// future refactor doesn't silently change null handling without updating
// every downstream consumer's expectations.
func TestPutTenant_ExplicitNullValueReturns200(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, 0.0)

	app := buildApp(t, c, allowAll(), allowAllTenant())

	resp := doRequest(t, app, http.MethodPut, "/system/global/fee.rate/tenants/tenant-A", `{"value":null}`)
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 for explicit null value (handler allows it), got %d: %s",
			resp.StatusCode, string(data))
	}
}

// ---------------------------------------------------------------------------
// Delete matrix.
// ---------------------------------------------------------------------------

// TestDeleteTenant_UnknownKeyReturns400 verifies that DELETE against an
// unregistered key surfaces 400 with "unknown_key" — the same sentinel
// mapping PUT uses, mirroring symmetry between the two mutating routes.
func TestDeleteTenant_UnknownKeyReturns400(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, 0.0)

	app := buildApp(t, c, allowAll(), allowAllTenant())

	resp := doRequest(t, app, http.MethodDelete, "/system/global/never-registered.key/tenants/tenant-A", "")
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusBadRequest {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 for unknown key on DELETE, got %d: %s",
			resp.StatusCode, string(data))
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	if body.Title != "unknown_key" {
		t.Fatalf("expected title 'unknown_key', got %q", body.Title)
	}
}

// TestDeleteTenant_InvalidTenantIDReturns400 mirrors
// TestPutTenant_InvalidTenantID_Returns400 on the DELETE route, locking the
// validation-before-handler invariant on DELETE too.
func TestDeleteTenant_InvalidTenantIDReturns400(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, 0.0)

	app := buildApp(t, c, allowAll(), allowAllTenant())

	resp := doRequest(t, app, http.MethodDelete, "/system/global/fee.rate/tenants/_global", "")
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	if body.Title != "invalid_tenant_id" {
		t.Fatalf("expected title 'invalid_tenant_id', got %q", body.Title)
	}
}

// ---------------------------------------------------------------------------
// URL encoding — characters in path segments.
// ---------------------------------------------------------------------------

// TestListTenants_KeyWithDotSegment verifies that a key containing a dot
// (e.g. "log.level") round-trips through Fiber's path parameter decoder
// unchanged. Dots are legal in keys; if Fiber's routing were mis-
// configured to treat them as path separators, this test would surface
// the regression.
//
// The test uses the GET tenants list route because it has the deepest
// path segment structure (:namespace/:key/tenants).
func TestListTenants_KeyWithDotSegment(t *testing.T) {
	t.Parallel()

	c, _ := buildClient(t)

	// Register a key with a dot — mirrors real Lerian keys like "log.level"
	// or "fees.fail_closed_default".
	if err := c.RegisterTenantScoped("global", "log.level", "info"); err != nil {
		t.Fatalf("RegisterTenantScoped: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Seed one tenant override so the list is non-empty. Going through the
	// Client (not the HTTP path) isolates the list route's decoding
	// behavior from the PUT route's.
	if err := c.SetForTenant(core.ContextWithTenantID(context.Background(), "tenant-A"),
		"global", "log.level", "debug", "admin"); err != nil {
		t.Fatalf("SetForTenant: %v", err)
	}

	app := buildApp(t, c, allowAll(), allowAllTenant())

	resp := doRequest(t, app, http.MethodGet, "/system/global/log.level/tenants", "")
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 for dotted key, got %d: %s", resp.StatusCode, string(data))
	}

	var body tenantListResp
	readJSON(t, resp, &body)

	if body.Key != "log.level" {
		t.Fatalf("key round-trip failed: expected 'log.level', got %q", body.Key)
	}

	if len(body.Tenants) != 1 || body.Tenants[0] != "tenant-A" {
		t.Fatalf("expected tenants=[tenant-A], got %v", body.Tenants)
	}
}

// ---------------------------------------------------------------------------
// Authorizer action + error propagation.
// ---------------------------------------------------------------------------

// TestTenantAuthorizer_ActionIsReadForGet verifies that a GET request to
// the tenants list route invokes the authorizer with action="read". Task 6
// covered the write-action PUT case and the empty-tenantID list case; this
// test locks that the list GET triggers "read" specifically.
func TestTenantAuthorizer_ActionIsReadForGet(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, 0.0)

	var (
		mu         sync.Mutex
		seenAction string
		callCount  int
	)

	authz := func(_ *fiber.Ctx, action, _ string) error {
		mu.Lock()
		defer mu.Unlock()
		seenAction = action
		callCount++
		return nil
	}

	app := buildApp(t, c, allowAll(), admin.WithTenantAuthorizer(authz))

	resp := doRequest(t, app, http.MethodGet, "/system/global/fee.rate/tenants", "")
	resp.Body.Close()

	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()

	if callCount != 1 {
		t.Fatalf("expected exactly one authorizer call, got %d", callCount)
	}

	if seenAction != "read" {
		t.Fatalf("expected action='read' for GET, got %q", seenAction)
	}
}

// TestTenantAuthorizer_ActionIsWriteForDelete verifies DELETE requests
// invoke the authorizer with action="write" (both PUT and DELETE mutate
// state, so both map to "write" under the action taxonomy).
func TestTenantAuthorizer_ActionIsWriteForDelete(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, 0.0)

	var (
		mu         sync.Mutex
		seenAction string
	)

	authz := func(_ *fiber.Ctx, action, _ string) error {
		mu.Lock()
		defer mu.Unlock()
		seenAction = action
		return nil
	}

	app := buildApp(t, c, allowAll(), admin.WithTenantAuthorizer(authz))

	resp := doRequest(t, app, http.MethodDelete, "/system/global/fee.rate/tenants/tenant-A", "")
	resp.Body.Close()

	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()

	if seenAction != "write" {
		t.Fatalf("expected action='write' for DELETE, got %q", seenAction)
	}
}

// TestTenantAuthorizer_ErrorPropagatesAs403 verifies that a non-nil return
// from the authorizer produces a 403 whose body carries the authorizer's
// error message. This is load-bearing: operators rely on the body message
// to diagnose WHICH policy rejected (RBAC role? tenant ownership? feature
// gate?), so dropping the message on the wire would blind every consumer.
func TestTenantAuthorizer_ErrorPropagatesAs403(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, 0.0)

	const reason = "tenant-A is frozen by compliance hold"

	authz := func(_ *fiber.Ctx, _, _ string) error {
		return errors.New(reason)
	}

	app := buildApp(t, c, allowAll(), admin.WithTenantAuthorizer(authz))

	resp := doRequest(t, app, http.MethodPut, "/system/global/fee.rate/tenants/tenant-A", `{"value":0.1}`)
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	if body.Message != reason {
		t.Fatalf("expected body message to carry authorizer error verbatim, got %q", body.Message)
	}

	if body.Code != fiber.StatusForbidden {
		t.Fatalf("expected body Code=403, got %d", body.Code)
	}
}

// TestTenantAuthorizer_DenyBeforeInvalidTenantIDOrder is an order-of-
// operations pin. admin.go's authorizeTenant middleware runs BEFORE the
// handler, so an invalid tenantID reaches authorizeTenant first — the
// authorizer sees the raw param. If the authorizer denies, no handler runs.
//
// This test verifies: an authorizer that denies on ANY tenantID is the
// first gate; even a malformed tenantID returns 403 (authorizer denies)
// rather than 400 (tenant ID invalid). This locks the audit trail: a
// denied authorizer always surfaces as 403.
func TestTenantAuthorizer_DenyBeforeInvalidTenantIDOrder(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, 0.0)

	var (
		mu     sync.Mutex
		denied bool
	)

	authz := func(_ *fiber.Ctx, _, _ string) error {
		mu.Lock()
		denied = true
		mu.Unlock()
		return errors.New("deny-all")
	}

	app := buildApp(t, c, allowAll(), admin.WithTenantAuthorizer(authz))

	// "_global" is an invalid tenant ID. If validation ran before auth,
	// this would be 400. Since auth runs first, it should be 403.
	resp := doRequest(t, app, http.MethodPut, "/system/global/fee.rate/tenants/_global", `{"value":0.1}`)
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403 (authorizer rejects before validation), got %d", resp.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()
	if !denied {
		t.Fatal("expected authorizer to be invoked even with a syntactically invalid tenantID")
	}
}
