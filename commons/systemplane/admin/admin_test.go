package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	commonshttp "github.com/LerianStudio/lib-commons/v5/commons/net/http"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/admin"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/gofiber/fiber/v2"
)

// ---------------------------------------------------------------------------
// In-memory fake TestStore
// ---------------------------------------------------------------------------

// fakeStore implements systemplane.TestStore entirely in memory. Set invokes
// all registered subscribe handlers synchronously after the map write,
// simulating a changefeed echo.
//
// Tenant-scoped rows live in a separate map keyed by (tenantID, ns, key) so
// the legacy namespace/key path stays unchanged and existing tests that
// never touch tenant methods continue to observe the pre-tenant behavior.
type fakeStore struct {
	mu           sync.Mutex
	entries      map[string]systemplane.TestEntry    // keyed by "namespace\x1fkey" — globals only
	tenantRows   map[tenantKey]systemplane.TestEntry // keyed by (tenantID, ns, key)
	handlers     []func(systemplane.TestEvent)
	closed       bool
	subscribedCh chan struct{} // closed when the first Subscribe registers
}

// tenantKey addresses a tenant-scoped row. This is scoped to the admin
// fakeStore and does not collide with the same-named type in the parent
// systemplane package's smoke tests.
type tenantKey struct {
	tenantID  string
	namespace string
	key       string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		entries:      make(map[string]systemplane.TestEntry),
		tenantRows:   make(map[tenantKey]systemplane.TestEntry),
		subscribedCh: make(chan struct{}),
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
	first := len(f.handlers) == 0
	f.handlers = append(f.handlers, handler)
	f.mu.Unlock()

	// Signal first-handler registration so tenant tests can wait for the
	// Client's Start() goroutine to plumb the subscriber through. Without
	// this, a PUT immediately after Start may race the subscribe handler
	// registration and lose its changefeed echo (tolerable for these
	// handler-level tests, but the signal costs nothing).
	if first {
		close(f.subscribedCh)
	}

	<-ctx.Done()

	return nil
}

func (f *fakeStore) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.closed = true

	return nil
}

// fireLocked fires a changefeed event to all subscribers. Caller must NOT
// hold f.mu — the handlers map is snapshotted under the lock, then released
// before firing so a handler that re-enters the store does not deadlock.
func (f *fakeStore) fire(evt systemplane.TestEvent) {
	f.mu.Lock()
	handlers := make([]func(systemplane.TestEvent), len(f.handlers))
	copy(handlers, f.handlers)
	f.mu.Unlock()

	for _, h := range handlers {
		h(evt)
	}
}

// ---------------------------------------------------------------------------
// Tenant-scoped TestStore methods — real implementations.
//
// The storage is partitioned: globals live in f.entries, tenant overrides
// live in f.tenantRows. A tenant-scoped write NEVER touches the globals
// map and vice versa, matching the production backend contract where the
// "_global" sentinel segregates rows at the database level.
// ---------------------------------------------------------------------------

func (f *fakeStore) GetTenantValue(_ context.Context, tenantID, namespace, key string) (systemplane.TestEntry, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	e, ok := f.tenantRows[tenantKey{tenantID: tenantID, namespace: namespace, key: key}]

	return e, ok, nil
}

func (f *fakeStore) SetTenantValue(_ context.Context, tenantID string, e systemplane.TestEntry) error {
	e.TenantID = tenantID

	f.mu.Lock()
	f.tenantRows[tenantKey{tenantID: tenantID, namespace: e.Namespace, key: e.Key}] = e
	f.mu.Unlock()

	// Fire a changefeed echo so OnTenantChange subscribers see the write.
	f.fire(systemplane.TestEvent{Namespace: e.Namespace, Key: e.Key, TenantID: tenantID})

	return nil
}

func (f *fakeStore) DeleteTenantValue(_ context.Context, tenantID, namespace, key, _ string) error {
	f.mu.Lock()
	delete(f.tenantRows, tenantKey{tenantID: tenantID, namespace: namespace, key: key})
	f.mu.Unlock()

	f.fire(systemplane.TestEvent{Namespace: namespace, Key: key, TenantID: tenantID})

	return nil
}

func (f *fakeStore) ListTenantValues(_ context.Context) ([]systemplane.TestEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]systemplane.TestEntry, 0, len(f.tenantRows))
	for _, e := range f.tenantRows {
		out = append(out, e)
	}

	return out, nil
}

func (f *fakeStore) ListTenantsForKey(_ context.Context, namespace, key string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	seen := make(map[string]struct{})

	for k := range f.tenantRows {
		if k.namespace == namespace && k.key == key {
			seen[k.tenantID] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}

	// Tests assert on sorted output; the real backends also return sorted
	// lists, so the fake preserves that contract to avoid false negatives.
	sort.Strings(out)

	return out, nil
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

// ---------------------------------------------------------------------------
// Task 6 — Tenant-scoped admin route tests
// ---------------------------------------------------------------------------

// tenantValueResp deserializes the PUT :key/tenants/:tenantID response body.
type tenantValueResp struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	TenantID  string `json:"tenant_id"`
	Value     any    `json:"value"`
}

// tenantListResp deserializes the GET :key/tenants response body.
type tenantListResp struct {
	Namespace string   `json:"namespace"`
	Key       string   `json:"key"`
	Tenants   []string `json:"tenants"`
}

// buildTenantClientStarted builds a started Client with one tenant-scoped key
// registered at ("global", "fee.rate") with the given default value, plus
// any extra KeyOptions. Returns the Client, the fakeStore, and waits for the
// Subscribe handler to register before returning so subsequent writes
// deterministically fire their changefeed echoes.
func buildTenantClientStarted(t *testing.T, defaultValue any, opts ...systemplane.KeyOption) (*systemplane.Client, *fakeStore) {
	t.Helper()

	c, fs := buildClient(t)

	if err := c.RegisterTenantScoped("global", "fee.rate", defaultValue, opts...); err != nil {
		t.Fatalf("RegisterTenantScoped: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for Subscribe to register before callers fire writes. Without
	// this, a tenant write immediately after Start may race the
	// subscribe goroutine's handler registration and miss the echo.
	select {
	case <-fs.subscribedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe handler did not register within 2s")
	}

	return c, fs
}

// allowAllTenant returns a MountOption that permits every tenant-route
// request, mirroring [allowAll] but for the tenant authorizer. Tests that
// want to exercise the tenant handlers without engaging the default-deny
// behavior use this.
func allowAllTenant() admin.MountOption {
	return admin.WithTenantAuthorizer(func(_ *fiber.Ctx, _, _ string) error { return nil })
}

func TestPutTenant_HappyPath(t *testing.T) {
	t.Parallel()

	c, fs := buildTenantClientStarted(t, 0.0)

	app := buildApp(t, c, allowAll(), allowAllTenant())

	resp := doRequest(t, app, http.MethodPut, "/system/global/fee.rate/tenants/tenant-A", `{"value":0.05}`)
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(data))
	}

	var body tenantValueResp
	readJSON(t, resp, &body)

	if body.Namespace != "global" || body.Key != "fee.rate" || body.TenantID != "tenant-A" {
		t.Fatalf("response path mismatch: got %+v", body)
	}

	// JSON round-trips numbers as float64.
	if body.Value != float64(0.05) {
		t.Fatalf("expected Value=0.05, got %v (%T)", body.Value, body.Value)
	}

	// Verify the store observed the write.
	stored, ok := fs.tenantRows[tenantKey{tenantID: "tenant-A", namespace: "global", key: "fee.rate"}]
	if !ok {
		t.Fatal("expected tenant row in fake store")
	}

	if stored.TenantID != "tenant-A" {
		t.Fatalf("expected stored TenantID='tenant-A', got %q", stored.TenantID)
	}

	// Verify via Client.GetForTenant that the value landed and is readable.
	v, found, err := c.GetForTenant(core.ContextWithTenantID(context.Background(), "tenant-A"), "global", "fee.rate")
	if err != nil {
		t.Fatalf("GetForTenant: %v", err)
	}

	if !found {
		t.Fatal("GetForTenant: expected found=true")
	}

	if v != 0.05 {
		t.Fatalf("GetForTenant: expected 0.05, got %v", v)
	}
}

func TestPutTenant_MissingAuthorizer_Returns403(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, 0.0)

	// Mount WITHOUT WithTenantAuthorizer. allowAll() configures only the
	// legacy authorizer; tenant routes MUST still deny by default.
	app := buildApp(t, c, allowAll())

	resp := doRequest(t, app, http.MethodPut, "/system/global/fee.rate/tenants/tenant-A", `{"value":0.05}`)
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusForbidden {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403, got %d: %s", resp.StatusCode, string(data))
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	if body.Code != fiber.StatusForbidden {
		t.Fatalf("expected body Code=403, got %d", body.Code)
	}

	// The default-deny message should surface in the body so operators can
	// diagnose the misconfiguration.
	if !strings.Contains(body.Message, "WithTenantAuthorizer") {
		t.Fatalf("expected default-deny message to mention WithTenantAuthorizer, got %q", body.Message)
	}
}

// TestListTenants_MissingAuthorizer_Returns403 exercises the tenant-list
// route's default-deny behavior. The list route passes tenantID="" to the
// authorizer; the default deny-all hook must still reject.
func TestListTenants_MissingAuthorizer_Returns403(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, 0.0)

	app := buildApp(t, c, allowAll()) // NO WithTenantAuthorizer.

	resp := doRequest(t, app, http.MethodGet, "/system/global/fee.rate/tenants", "")
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestPutTenant_InvalidTenantID_Returns400(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, 0.0)

	app := buildApp(t, c, allowAll(), allowAllTenant())

	// "_global" is the reserved sentinel; IsValidTenantID rejects it
	// (first char "_" is non-alphanumeric) AND admin.validateTenantIDParam
	// explicitly blocks it with a dedicated error message.
	resp := doRequest(t, app, http.MethodPut, "/system/global/fee.rate/tenants/_global", `{"value":0.05}`)
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusBadRequest {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400, got %d: %s", resp.StatusCode, string(data))
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	if body.Title != "invalid_tenant_id" {
		t.Fatalf("expected title 'invalid_tenant_id', got %q", body.Title)
	}

	// The sentinel-specific message should mention "_global" so operators
	// can diagnose the collision without reading source.
	if !strings.Contains(body.Message, "_global") {
		t.Fatalf("expected message to mention '_global' sentinel, got %q", body.Message)
	}
}

// TestPutTenant_InvalidTenantID_SpecialChars verifies the regex validator
// rejects tenant IDs with disallowed characters (the admin layer blocks
// these BEFORE invoking authorization, so a malformed path never reaches
// the Client).
func TestPutTenant_InvalidTenantID_SpecialChars(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, 0.0)

	app := buildApp(t, c, allowAll(), allowAllTenant())

	// tenant-manager/core's validTenantIDPattern requires the first
	// character to be alphanumeric. Leading hyphen must be rejected.
	resp := doRequest(t, app, http.MethodPut, "/system/global/fee.rate/tenants/-badlead", `{"value":0.05}`)
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("expected 400 for leading-hyphen tenantID, got %d", resp.StatusCode)
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	if body.Title != "invalid_tenant_id" {
		t.Fatalf("expected title 'invalid_tenant_id', got %q", body.Title)
	}
}

func TestPutTenant_UnknownKey_Returns400(t *testing.T) {
	t.Parallel()

	// Client is started with fee.rate registered as tenant-scoped, but the
	// request targets a namespace/key pair that was never registered at
	// all — Client.requireTenantScoped returns ErrUnknownKey.
	c, _ := buildTenantClientStarted(t, 0.0)

	app := buildApp(t, c, allowAll(), allowAllTenant())

	resp := doRequest(t, app, http.MethodPut, "/system/global/unknown.key/tenants/tenant-A", `{"value":1}`)
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusBadRequest {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400, got %d: %s", resp.StatusCode, string(data))
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	if body.Title != "unknown_key" {
		t.Fatalf("expected title 'unknown_key', got %q", body.Title)
	}
}

// TestPutTenant_NonTenantScopedKey verifies that writing a tenant override
// for a key registered with the legacy Register (not RegisterTenantScoped)
// returns ErrTenantScopeNotRegistered → 400 with the tenant_scope_not_registered
// title.
func TestPutTenant_NonTenantScopedKey(t *testing.T) {
	t.Parallel()

	c, _ := buildClient(t)

	// Register via legacy Register, NOT RegisterTenantScoped.
	if err := c.Register("global", "legacy.key", "v"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	app := buildApp(t, c, allowAll(), allowAllTenant())

	resp := doRequest(t, app, http.MethodPut, "/system/global/legacy.key/tenants/tenant-A", `{"value":"x"}`)
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusBadRequest {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400, got %d: %s", resp.StatusCode, string(data))
	}

	var body commonshttp.ErrorResponse
	readJSON(t, resp, &body)

	if body.Title != "tenant_scope_not_registered" {
		t.Fatalf("expected title 'tenant_scope_not_registered', got %q", body.Title)
	}
}

func TestDeleteTenant_HappyPath(t *testing.T) {
	t.Parallel()

	// Register the key with a distinctive default so the fallthrough is
	// observable after delete.
	c, fs := buildTenantClientStarted(t, 0.42)

	app := buildApp(t, c, allowAll(), allowAllTenant())

	// 1. Write an override.
	putResp := doRequest(t, app, http.MethodPut, "/system/global/fee.rate/tenants/tenant-A", `{"value":0.99}`)
	putResp.Body.Close()

	if putResp.StatusCode != fiber.StatusOK {
		t.Fatalf("PUT preconditions: expected 200, got %d", putResp.StatusCode)
	}

	// Sanity: the override should be in the store.
	if _, ok := fs.tenantRows[tenantKey{tenantID: "tenant-A", namespace: "global", key: "fee.rate"}]; !ok {
		t.Fatal("preconditions: tenant row should exist after PUT")
	}

	// 2. Delete via HTTP.
	delResp := doRequest(t, app, http.MethodDelete, "/system/global/fee.rate/tenants/tenant-A", "")
	defer delResp.Body.Close()

	if delResp.StatusCode != fiber.StatusNoContent {
		data, _ := io.ReadAll(delResp.Body)
		t.Fatalf("DELETE: expected 204, got %d: %s", delResp.StatusCode, string(data))
	}

	// 3. Store should no longer have the override.
	if _, ok := fs.tenantRows[tenantKey{tenantID: "tenant-A", namespace: "global", key: "fee.rate"}]; ok {
		t.Fatal("expected tenant row to be gone after DELETE")
	}

	// 4. GetForTenant should fall through to the registered default (0.42)
	// since neither a tenant override nor a global Set is in play.
	v, found, err := c.GetForTenant(core.ContextWithTenantID(context.Background(), "tenant-A"), "global", "fee.rate")
	if err != nil {
		t.Fatalf("GetForTenant: %v", err)
	}

	if !found {
		t.Fatal("GetForTenant after delete: expected found=true")
	}

	if v != 0.42 {
		t.Fatalf("GetForTenant after delete: expected default 0.42, got %v", v)
	}
}

// TestDeleteTenant_Idempotent verifies that deleting a non-existent override
// is NOT an error — it returns 204 just like a successful delete. This
// mirrors Client.DeleteForTenant's contract (backend delete is idempotent).
func TestDeleteTenant_Idempotent(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, 0.0)

	app := buildApp(t, c, allowAll(), allowAllTenant())

	// No prior write — delete against a nonexistent override.
	resp := doRequest(t, app, http.MethodDelete, "/system/global/fee.rate/tenants/tenant-A", "")
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusNoContent {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 204 for idempotent delete, got %d: %s", resp.StatusCode, string(data))
	}
}

func TestListTenants_ReturnsSortedList(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, 0.0)

	// Seed three overrides in non-alphabetical order via the Client (so
	// both the changefeed path and the store path are exercised).
	tenants := []string{"tenant-C", "tenant-A", "tenant-B"}
	for _, id := range tenants {
		ctx := core.ContextWithTenantID(context.Background(), id)
		if err := c.SetForTenant(ctx, "global", "fee.rate", 0.01, "admin"); err != nil {
			t.Fatalf("SetForTenant(%s): %v", id, err)
		}
	}

	app := buildApp(t, c, allowAll(), allowAllTenant())

	resp := doRequest(t, app, http.MethodGet, "/system/global/fee.rate/tenants", "")
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(data))
	}

	var body tenantListResp
	readJSON(t, resp, &body)

	if body.Namespace != "global" || body.Key != "fee.rate" {
		t.Fatalf("response path mismatch: got namespace=%q key=%q", body.Namespace, body.Key)
	}

	expected := []string{"tenant-A", "tenant-B", "tenant-C"}
	if len(body.Tenants) != len(expected) {
		t.Fatalf("expected %d tenants, got %d: %v", len(expected), len(body.Tenants), body.Tenants)
	}

	for i, want := range expected {
		if body.Tenants[i] != want {
			t.Fatalf("tenants[%d]: expected %q, got %q (full list: %v)", i, want, body.Tenants[i], body.Tenants)
		}
	}
}

// TestListTenants_EmptyList verifies the empty-state response: a registered
// tenant-scoped key with no overrides returns 200 + an empty slice (NOT null
// in JSON, which would break JS clients expecting an iterable).
func TestListTenants_EmptyList(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, 0.0)

	app := buildApp(t, c, allowAll(), allowAllTenant())

	resp := doRequest(t, app, http.MethodGet, "/system/global/fee.rate/tenants", "")
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Decode the raw bytes to inspect the wire format — we want to see "[]"
	// not "null" for the empty tenants field.
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	var body tenantListResp
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("Unmarshal(%s): %v", string(data), err)
	}

	if body.Tenants == nil {
		t.Fatalf("expected non-nil tenants slice, raw body: %s", string(data))
	}

	if len(body.Tenants) != 0 {
		t.Fatalf("expected empty tenants slice, got %v", body.Tenants)
	}

	// Verify wire format: JSON should contain `"tenants":[]`, not
	// `"tenants":null`.
	if !strings.Contains(string(data), `"tenants":[]`) {
		t.Fatalf("expected wire format to contain '\"tenants\":[]', got: %s", string(data))
	}
}

// TestPutTenant_TenantAuthorizerReceivesTenantID verifies that the tenantID
// URL parameter is propagated to the authorizer hook. This is the load-
// bearing behavior callers will build tenant-specific policy on top of (e.g.
// "allow write only if caller owns :tenantID").
func TestPutTenant_TenantAuthorizerReceivesTenantID(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, 0.0)

	var (
		mu           sync.Mutex
		seenAction   string
		seenTenantID string
		callCount    int
	)

	authz := func(_ *fiber.Ctx, action, tenantID string) error {
		mu.Lock()
		seenAction = action
		seenTenantID = tenantID
		callCount++
		mu.Unlock()

		return nil
	}

	app := buildApp(t, c, allowAll(), admin.WithTenantAuthorizer(authz))

	resp := doRequest(t, app, http.MethodPut, "/system/global/fee.rate/tenants/tenant-X", `{"value":1.23}`)
	resp.Body.Close()

	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()

	if callCount != 1 {
		t.Fatalf("expected authorizer to be called exactly once, got %d calls", callCount)
	}

	if seenAction != "write" {
		t.Fatalf("expected action='write', got %q", seenAction)
	}

	if seenTenantID != "tenant-X" {
		t.Fatalf("expected tenantID='tenant-X', got %q", seenTenantID)
	}
}

// TestListTenants_AuthorizerReceivesEmptyTenantID verifies the list route
// invokes the authorizer with tenantID="" (no :tenantID segment in the path).
// Policies can use this sentinel to distinguish "list tenants" from
// per-tenant actions.
func TestListTenants_AuthorizerReceivesEmptyTenantID(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, 0.0)

	var (
		mu           sync.Mutex
		seenTenantID string
		seenAction   string
	)

	authz := func(_ *fiber.Ctx, action, tenantID string) error {
		mu.Lock()
		seenTenantID = tenantID
		seenAction = action
		mu.Unlock()

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

	if seenTenantID != "" {
		t.Fatalf("expected empty tenantID for list route, got %q", seenTenantID)
	}

	if seenAction != "read" {
		t.Fatalf("expected action='read', got %q", seenAction)
	}
}

// TestPutTenant_AppliesRedaction verifies that a key registered with
// RedactFull round-trips through the PUT handler's response with the
// redacted value, not the plaintext the caller submitted.
func TestPutTenant_AppliesRedaction(t *testing.T) {
	t.Parallel()

	c, _ := buildTenantClientStarted(t, "initial", systemplane.WithRedaction(systemplane.RedactFull))

	app := buildApp(t, c, allowAll(), allowAllTenant())

	resp := doRequest(t, app, http.MethodPut, "/system/global/fee.rate/tenants/tenant-A", `{"value":"hunter2"}`)
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(data))
	}

	var body tenantValueResp
	readJSON(t, resp, &body)

	if body.Value != "[REDACTED]" {
		t.Fatalf("expected redacted response value, got %v", body.Value)
	}
}
