package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	commonshttp "github.com/LerianStudio/lib-commons/v5/commons/net/http"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/admin"
	"github.com/gofiber/fiber/v2"
)

// ---------------------------------------------------------------------------
// In-memory fake TestStore
// ---------------------------------------------------------------------------

// fakeStore implements systemplane.TestStore entirely in memory. Set invokes
// all registered subscribe handlers synchronously after the map write,
// simulating a changefeed echo.
type fakeStore struct {
	mu       sync.Mutex
	entries  map[string]systemplane.TestEntry // keyed by "namespace\x1fkey"
	handlers []func(systemplane.TestEvent)
	closed   bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		entries: make(map[string]systemplane.TestEntry),
	}
}

func storeKey(ns, key string) string { return ns + "\x1f" + key }

func (f *fakeStore) List(_ context.Context) ([]systemplane.TestEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]systemplane.TestEntry, 0, len(f.entries))
	for _, e := range f.entries {
		out = append(out, e)
	}

	return out, nil
}

func (f *fakeStore) Get(_ context.Context, namespace, key string) (systemplane.TestEntry, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	e, ok := f.entries[storeKey(namespace, key)]

	return e, ok, nil
}

func (f *fakeStore) Set(_ context.Context, e systemplane.TestEntry) error {
	f.mu.Lock()
	f.entries[storeKey(e.Namespace, e.Key)] = e

	handlers := make([]func(systemplane.TestEvent), len(f.handlers))
	copy(handlers, f.handlers)
	f.mu.Unlock()

	// Fire changefeed echo synchronously.
	evt := systemplane.TestEvent{Namespace: e.Namespace, Key: e.Key}
	for _, h := range handlers {
		h(evt)
	}

	return nil
}

func (f *fakeStore) Subscribe(ctx context.Context, handler func(systemplane.TestEvent)) error {
	f.mu.Lock()
	f.handlers = append(f.handlers, handler)
	f.mu.Unlock()

	<-ctx.Done()

	return nil
}

func (f *fakeStore) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.closed = true

	return nil
}

// Tenant-scoped methods — Task 1 no-op stubs so fakeStore satisfies TestStore.
// Real tenant semantics are exercised by the TestStore-backed tests in Task 7.

func (f *fakeStore) GetTenantValue(_ context.Context, _, _, _ string) (systemplane.TestEntry, bool, error) {
	return systemplane.TestEntry{}, false, nil
}

func (f *fakeStore) SetTenantValue(_ context.Context, _ string, _ systemplane.TestEntry) error {
	return nil
}

func (f *fakeStore) DeleteTenantValue(_ context.Context, _, _, _, _ string) error {
	return nil
}

func (f *fakeStore) ListTenantValues(_ context.Context) ([]systemplane.TestEntry, error) {
	return nil, nil
}

func (f *fakeStore) ListTenantsForKey(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}

// lastEntry returns the last-written entry for a (namespace, key) pair.
func (f *fakeStore) lastEntry(namespace, key string) (systemplane.TestEntry, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	e, ok := f.entries[storeKey(namespace, key)]

	return e, ok
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildClient creates a Client + fakeStore for testing, wired through NewForTesting.
func buildClient(t *testing.T) (*systemplane.Client, *fakeStore) {
	t.Helper()

	fs := newFakeStore()

	c, err := systemplane.NewForTesting(fs, systemplane.WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("NewForTesting: %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	return c, fs
}

// buildClientStarted creates a Client that is already started.
func buildClientStarted(t *testing.T) (*systemplane.Client, *fakeStore) {
	t.Helper()

	c, fs := buildClient(t)

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	return c, fs
}

// buildApp creates a Fiber app with admin routes mounted.
func buildApp(t *testing.T, c *systemplane.Client, opts ...admin.MountOption) *fiber.App {
	t.Helper()

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	admin.Mount(app, c, opts...)

	return app
}

// doRequest performs an HTTP request against the Fiber app and returns the response.
func doRequest(t *testing.T, app *fiber.App, method, path string, body string) *http.Response {
	t.Helper()

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, path, bodyReader)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}

	return resp
}

// readJSON decodes the response body into v.
func readJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()

	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("Unmarshal(%s): %v", string(data), err)
	}
}

// ---------------------------------------------------------------------------
// Response DTOs for deserialization
// ---------------------------------------------------------------------------

type listResp struct {
	Namespace string      `json:"namespace"`
	Entries   []entryResp `json:"entries"`
}

type entryResp struct {
	Key         string `json:"key"`
	Value       any    `json:"value"`
	Description string `json:"description,omitempty"`
}

type getResp struct {
	Namespace   string `json:"namespace"`
	Key         string `json:"key"`
	Value       any    `json:"value"`
	Description string `json:"description,omitempty"`
}

// allowAll returns a MountOption that permits every request. Tests use this to
// opt in to the old allow-all behavior now that the default authorizer is
// deny-all (secure by default).
func allowAll() admin.MountOption {
	return admin.WithAuthorizer(func(_ *fiber.Ctx, _ string) error { return nil })
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestMount_NoopOnNilClient(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{DisableStartupMessage: true})

	// Mount with nil client should not panic and should not register routes.
	admin.Mount(app, nil)

	resp := doRequest(t, app, http.MethodGet, "/system/global", "")
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("expected 404 (no routes registered), got %d", resp.StatusCode)
	}
}

func TestMount_DefaultPrefix(t *testing.T) {
	t.Parallel()

	c, _ := buildClient(t)

	// Register a key so the namespace exists but has entries.
	if err := c.Register("global", "log.level", "info"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	app := buildApp(t, c, allowAll())

	resp := doRequest(t, app, http.MethodGet, "/system/global", "")

	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body listResp
	readJSON(t, resp, &body)

	if body.Namespace != "global" {
		t.Fatalf("expected namespace 'global', got %q", body.Namespace)
	}
}

func TestMount_CustomPrefix(t *testing.T) {
	t.Parallel()

	c, _ := buildClientStarted(t)

	app := buildApp(t, c, allowAll(), admin.WithPathPrefix("/cfg"))

	// Default prefix should NOT be registered.
	resp := doRequest(t, app, http.MethodGet, "/system/global", "")
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("expected 404 at /system, got %d", resp.StatusCode)
	}

	// Custom prefix should be registered.
	resp2 := doRequest(t, app, http.MethodGet, "/cfg/global", "")

	if resp2.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 at /cfg/global, got %d", resp2.StatusCode)
	}

	resp2.Body.Close()
}

func TestGetList_ReturnsSortedEntries(t *testing.T) {
	t.Parallel()

	c, _ := buildClient(t)

	// Register 3 keys in "global" (out of alphabetical order).
	if err := c.Register("global", "rate_limit.rps", 100); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Register("global", "log.level", "info"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Register("global", "feature.enabled", true); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Set values on two of the three.
	if err := c.Set(context.Background(), "global", "log.level", "debug", "ops"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := c.Set(context.Background(), "global", "rate_limit.rps", 200, "ops"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	app := buildApp(t, c, allowAll())
	resp := doRequest(t, app, http.MethodGet, "/system/global", "")

	var body listResp
	readJSON(t, resp, &body)

	if len(body.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(body.Entries))
	}

	// Expect sorted order: feature.enabled, log.level, rate_limit.rps.
	expected := []string{"feature.enabled", "log.level", "rate_limit.rps"}
	for i, e := range body.Entries {
		if e.Key != expected[i] {
			t.Fatalf("entry[%d]: expected key %q, got %q", i, expected[i], e.Key)
		}
	}

	// Verify values: feature.enabled=true (default), log.level=debug (set), rate_limit.rps=200 (set).
	if body.Entries[0].Value != true {
		t.Fatalf("feature.enabled: expected true, got %v", body.Entries[0].Value)
	}

	if body.Entries[1].Value != "debug" {
		t.Fatalf("log.level: expected 'debug', got %v", body.Entries[1].Value)
	}

	// JSON numbers are float64.
	if body.Entries[2].Value != float64(200) {
		t.Fatalf("rate_limit.rps: expected 200, got %v", body.Entries[2].Value)
	}
}

func TestGetList_AppliesRedaction(t *testing.T) {
	t.Parallel()

	c, _ := buildClient(t)

	if err := c.Register("global", "secret.key", "my-secret", systemplane.WithRedaction(systemplane.RedactFull)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Register("global", "masked.key", "partial", systemplane.WithRedaction(systemplane.RedactMask)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Register("global", "plain.key", "visible"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	app := buildApp(t, c, allowAll())
	resp := doRequest(t, app, http.MethodGet, "/system/global", "")

	var body listResp
	readJSON(t, resp, &body)

	if len(body.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(body.Entries))
	}

	// Entries are sorted: masked.key, plain.key, secret.key.
	entryMap := make(map[string]any, len(body.Entries))
	for _, e := range body.Entries {
		entryMap[e.Key] = e.Value
	}

	if entryMap["secret.key"] != "[REDACTED]" {
		t.Fatalf("secret.key: expected '[REDACTED]', got %v", entryMap["secret.key"])
	}

	if entryMap["masked.key"] != "****" {
		t.Fatalf("masked.key: expected '****', got %v", entryMap["masked.key"])
	}

	if entryMap["plain.key"] != "visible" {
		t.Fatalf("plain.key: expected 'visible', got %v", entryMap["plain.key"])
	}
}

func TestGetOne_Success(t *testing.T) {
	t.Parallel()

	c, _ := buildClient(t)

	if err := c.Register("global", "log.level", "info"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := c.Set(context.Background(), "global", "log.level", "debug", "ops"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	app := buildApp(t, c, allowAll())
	resp := doRequest(t, app, http.MethodGet, "/system/global/log.level", "")

	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body getResp
	readJSON(t, resp, &body)

	if body.Namespace != "global" {
		t.Fatalf("expected namespace 'global', got %q", body.Namespace)
	}

	if body.Key != "log.level" {
		t.Fatalf("expected key 'log.level', got %q", body.Key)
	}

	if body.Value != "debug" {
		t.Fatalf("expected value 'debug', got %v", body.Value)
	}
}

func TestGetOne_NotFound(t *testing.T) {
	t.Parallel()

	c, _ := buildClientStarted(t)

	app := buildApp(t, c, allowAll())
	resp := doRequest(t, app, http.MethodGet, "/system/global/nonexistent", "")

	if resp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	if body.Code != fiber.StatusNotFound {
		t.Fatalf("expected code 404, got %d", body.Code)
	}

	if body.Title != "not_found" {
		t.Fatalf("expected title 'not_found', got %q", body.Title)
	}
}

func TestPut_Success(t *testing.T) {
	t.Parallel()

	c, _ := buildClient(t)

	if err := c.Register("global", "log.level", "info"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	app := buildApp(t, c, allowAll())
	resp := doRequest(t, app, http.MethodPut, "/system/global/log.level", `{"value":"debug"}`)
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusNoContent {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 204, got %d: %s", resp.StatusCode, string(data))
	}

	// Verify the value was written through to the Client.
	v, ok := c.Get("global", "log.level")
	if !ok {
		t.Fatal("expected key to exist after PUT")
	}

	if v != "debug" {
		t.Fatalf("expected 'debug', got %v", v)
	}
}

func TestPut_ValidationFailure(t *testing.T) {
	t.Parallel()

	c, _ := buildClient(t)

	validator := func(v any) error {
		s, ok := v.(string)
		if !ok {
			return errors.New("must be string")
		}

		allowed := map[string]bool{"info": true, "debug": true, "warn": true, "error": true}
		if !allowed[s] {
			return errors.New("invalid log level")
		}

		return nil
	}

	if err := c.Register("global", "log.level", "info", systemplane.WithValidator(validator)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	app := buildApp(t, c, allowAll())
	resp := doRequest(t, app, http.MethodPut, "/system/global/log.level", `{"value":"trace"}`)

	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	if body.Title != "validation_error" {
		t.Fatalf("expected title 'validation_error', got %q", body.Title)
	}
}

func TestPut_UnknownKey(t *testing.T) {
	t.Parallel()

	c, _ := buildClient(t)

	// Register at least one key so Start works.
	if err := c.Register("global", "known", "v"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	app := buildApp(t, c, allowAll())
	resp := doRequest(t, app, http.MethodPut, "/system/global/unknown", `{"value":"x"}`)

	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	if body.Title != "unknown_key" {
		t.Fatalf("expected title 'unknown_key', got %q", body.Title)
	}
}

func TestMount_DefaultAuthorizer_DeniesAll(t *testing.T) {
	t.Parallel()

	c, _ := buildClientStarted(t)

	// No allowAll(), no WithAuthorizer — uses the default deny-all authorizer.
	app := buildApp(t, c)

	// GET should be denied.
	resp := doRequest(t, app, http.MethodGet, "/system/global", "")

	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("GET: expected 403, got %d", resp.StatusCode)
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	if body.Code != fiber.StatusForbidden {
		t.Fatalf("GET: expected code 403, got %d", body.Code)
	}

	// PUT should be denied.
	resp = doRequest(t, app, http.MethodPut, "/system/global/some-key", `{"value":"x"}`)

	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("PUT: expected 403, got %d", resp.StatusCode)
	}

	readJSON(t, resp, &body)

	if body.Code != fiber.StatusForbidden {
		t.Fatalf("PUT: expected code 403, got %d", body.Code)
	}
}

func TestAuthorizer_DeniesRead(t *testing.T) {
	t.Parallel()

	c, _ := buildClientStarted(t)

	authz := func(_ *fiber.Ctx, action string) error {
		if action == "read" {
			return errors.New("no read access")
		}

		return nil
	}

	app := buildApp(t, c, admin.WithAuthorizer(authz))
	resp := doRequest(t, app, http.MethodGet, "/system/global", "")

	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	if body.Code != fiber.StatusForbidden {
		t.Fatalf("expected code 403, got %d", body.Code)
	}
}

func TestAuthorizer_DeniesWrite(t *testing.T) {
	t.Parallel()

	c, _ := buildClient(t)

	if err := c.Register("global", "k", "v"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	authz := func(_ *fiber.Ctx, action string) error {
		if action == "write" {
			return errors.New("no write access")
		}

		return nil
	}

	app := buildApp(t, c, admin.WithAuthorizer(authz))
	resp := doRequest(t, app, http.MethodPut, "/system/global/k", `{"value":"new"}`)

	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	if body.Code != fiber.StatusForbidden {
		t.Fatalf("expected code 403, got %d", body.Code)
	}
}

func TestActorExtractor_PropagatesToSet(t *testing.T) {
	t.Parallel()

	c, fs := buildClient(t)

	if err := c.Register("global", "log.level", "info"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	extractor := func(_ *fiber.Ctx) string { return "ops-1" }

	app := buildApp(t, c, allowAll(), admin.WithActorExtractor(extractor))
	resp := doRequest(t, app, http.MethodPut, "/system/global/log.level", `{"value":"debug"}`)
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusNoContent {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 204, got %d: %s", resp.StatusCode, string(data))
	}

	entry, ok := fs.lastEntry("global", "log.level")
	if !ok {
		t.Fatal("expected entry in fake store")
	}

	if entry.UpdatedBy != "ops-1" {
		t.Fatalf("expected UpdatedBy='ops-1', got %q", entry.UpdatedBy)
	}
}

func TestPut_ServiceUnavailable_WhenNotStarted(t *testing.T) {
	t.Parallel()

	c, _ := buildClient(t)

	// Register a key but do NOT call Start.
	if err := c.Register("global", "k", "v"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	app := buildApp(t, c, allowAll())
	resp := doRequest(t, app, http.MethodPut, "/system/global/k", `{"value":"new"}`)

	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	if body.Title != "service_unavailable" {
		t.Fatalf("expected title 'service_unavailable', got %q", body.Title)
	}
}

// ---------------------------------------------------------------------------
// Bonus edge case tests
// ---------------------------------------------------------------------------

func TestGetOne_AppliesRedaction(t *testing.T) {
	t.Parallel()

	c, _ := buildClient(t)

	if err := c.Register("global", "secret", "hunter2", systemplane.WithRedaction(systemplane.RedactMask)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	app := buildApp(t, c, allowAll())
	resp := doRequest(t, app, http.MethodGet, "/system/global/secret", "")

	var body getResp
	readJSON(t, resp, &body)

	if body.Value != "****" {
		t.Fatalf("expected '****', got %v", body.Value)
	}
}

func TestGetList_EmptyNamespace(t *testing.T) {
	t.Parallel()

	c, _ := buildClientStarted(t)

	app := buildApp(t, c, allowAll())
	resp := doRequest(t, app, http.MethodGet, "/system/empty", "")

	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body listResp
	readJSON(t, resp, &body)

	if body.Namespace != "empty" {
		t.Fatalf("expected namespace 'empty', got %q", body.Namespace)
	}

	if body.Entries == nil {
		t.Fatal("expected non-nil entries slice")
	}

	if len(body.Entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(body.Entries))
	}
}

func TestPut_InvalidBody(t *testing.T) {
	t.Parallel()

	c, _ := buildClient(t)

	if err := c.Register("global", "k", "v"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	app := buildApp(t, c, allowAll())

	// No body at all.
	resp := doRequest(t, app, http.MethodPut, "/system/global/k", "")

	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for empty body, got %d", resp.StatusCode)
	}

	resp.Body.Close()

	// Malformed JSON.
	resp2 := doRequest(t, app, http.MethodPut, "/system/global/k", "{invalid")

	if resp2.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for malformed JSON, got %d", resp2.StatusCode)
	}

	resp2.Body.Close()
}

func TestPut_MissingValueField(t *testing.T) {
	t.Parallel()

	c, _ := buildClient(t)

	if err := c.Register("global", "k", "v"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	app := buildApp(t, c, allowAll())

	// Empty object — "value" field is absent.
	resp := doRequest(t, app, http.MethodPut, "/system/global/k", `{}`)

	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for missing value field, got %d", resp.StatusCode)
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	if body.Title != "bad_request" {
		t.Fatalf("expected title 'bad_request', got %q", body.Title)
	}

	if body.Message != "missing value field" {
		t.Fatalf("expected message 'missing value field', got %q", body.Message)
	}
}

func TestPut_ExplicitNullValue(t *testing.T) {
	t.Parallel()

	c, _ := buildClient(t)

	if err := c.Register("global", "k", "v"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	app := buildApp(t, c, allowAll())

	// Explicit null — "value" is present but null; should be accepted.
	resp := doRequest(t, app, http.MethodPut, "/system/global/k", `{"value":null}`)
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusNoContent {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 204 for explicit null value, got %d: %s", resp.StatusCode, string(data))
	}

	// Verify the value was stored as nil.
	v, ok := c.Get("global", "k")
	if !ok {
		t.Fatal("expected key to exist after PUT with explicit null")
	}

	if v != nil {
		t.Fatalf("expected nil value, got %v", v)
	}
}

func TestWithPathPrefix_LeadingSlashOptional(t *testing.T) {
	t.Parallel()

	c, _ := buildClientStarted(t)

	// Without leading slash.
	app := buildApp(t, c, allowAll(), admin.WithPathPrefix("cfg"))
	resp := doRequest(t, app, http.MethodGet, "/cfg/global", "")

	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 at /cfg/global, got %d", resp.StatusCode)
	}

	resp.Body.Close()
}

func TestWithPathPrefix_TrailingSlashNormalized(t *testing.T) {
	t.Parallel()

	c, _ := buildClientStarted(t)

	// With trailing slash.
	app := buildApp(t, c, allowAll(), admin.WithPathPrefix("/api/"))
	resp := doRequest(t, app, http.MethodGet, "/api/global", "")

	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 at /api/global, got %d", resp.StatusCode)
	}

	resp.Body.Close()
}

// TestNewForTesting_NilStoreReturnsError verifies the guard in NewForTesting.
func TestNewForTesting_NilStoreReturnsError(t *testing.T) {
	t.Parallel()

	_, err := systemplane.NewForTesting(nil)
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}

// TestKeyRedaction_NilClient verifies nil-safety.
func TestKeyRedaction_NilClient(t *testing.T) {
	t.Parallel()

	var c *systemplane.Client

	if p := c.KeyRedaction("ns", "k"); p != systemplane.RedactNone {
		t.Fatalf("expected RedactNone for nil client, got %v", p)
	}
}

// TestList_NilClient verifies nil-safety.
func TestList_NilClient(t *testing.T) {
	t.Parallel()

	var c *systemplane.Client

	entries := c.List("ns")
	if entries != nil {
		t.Fatalf("expected nil for nil client, got %v", entries)
	}
}

// Ensure fakeStore.lastEntry has _some_ reference to the UpdatedBy field
// being propagated through the adapter. This validates the full adapter path:
// admin → Client.Set → store adapter → fakeStore.Set.
func TestActorExtractor_VerifiesAdapterPath(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()

	c, err := systemplane.NewForTesting(fs, systemplane.WithLogger(log.NewNop()))
	if err != nil {
		t.Fatalf("NewForTesting: %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	if err := c.Register("ns", "k", "v"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Write directly through the Client.
	if err := c.Set(context.Background(), "ns", "k", "new", "test-actor"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	entry, ok := fs.lastEntry("ns", "k")
	if !ok {
		t.Fatal("expected entry in store")
	}

	if entry.UpdatedBy != "test-actor" {
		t.Fatalf("expected UpdatedBy='test-actor', got %q", entry.UpdatedBy)
	}

	if !entry.UpdatedAt.After(time.Time{}) {
		t.Fatal("expected non-zero UpdatedAt")
	}
}
