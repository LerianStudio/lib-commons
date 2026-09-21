# Idempotency middleware: consumer gaps measured by Matcher (fix plan)

> **For implementers:** one lane, one branch (`fix/idempotency-fingerprint-provider-and-replay-headers`, base `develop`), one PR. Each task is dispatch-ready; run them in order. Every behaviour change starts with a RED test in `commons/net/http/idempotency`.

**Goal:** three defects of `commons/net/http/idempotency`, found by Matcher when it measured the middleware against its own behavioural suite, are fixed on the v7 line without changing any existing default.

**Architecture:** the middleware keeps its state machine; each fix is an option or a local change in `idempotency.go` with tests beside the existing ones. No new package, no new dependency.

**Tech Stack:** Go 1.26, Fiber v3, testify, miniredis (already used by the package tests).

## Lane operating rules

1. Worktree `/srv/worktrees/idempotency-fingerprint-replay`, branch `fix/idempotency-fingerprint-provider-and-replay-headers`, cut from `origin/develop`. PRs into `main` are accepted only from `develop`, so the PR base is `develop`.
2. Conventional Commits; the scope for this package is `net` (the repo's allowed scopes include `net`). Titles: `fix(net): ...` for each defect, `test(net): ...` for test-only commits, `docs: ...` for this file.
3. Every commit signed (`commit.gpgsign=true` is configured; confirm with `git log --show-signature -1`).
4. Frozen: every existing option keeps its name, signature and default. `MIGRATION-v7.md` and `CHANGELOG.md` are not edited (the release pipeline writes the changelog). `go.mod` and `go.sum` are not edited.
5. Verification per task, then for the lane: `go build ./... && go vet -tags=unit ./commons/net/http/idempotency/... && go test -tags=unit -race -count=1 ./commons/net/http/idempotency/...` green; `golangci-lint run ./commons/net/http/idempotency/...` reports no new finding against the baseline you measure first.
6. Never generate synthetic CPU load in a test.

## Bug protocol

A defect in this package found while working: RED test, fix, `fix(net):` commit in this PR, one line in `## Bugs found` below. A defect elsewhere in lib-commons: one line in `## Bugs found outside this package` with file, symptom and evidence; do not edit it.

## Ownership carve-outs

This lane owns `commons/net/http/idempotency/**` and this file. Nothing else.

## Phase Overview

| Phase | Milestone | Epics | Status |
|-------|-----------|-------|--------|
| 1 | a consumer with streamed or multipart requests, global response-header middleware, or success bodies above the cache cap can mount the middleware without re-executing a mutation or duplicating headers | 1.1 | Detailed |

## Phase 1

### Epic 1.1: Three consumer gaps closed on the v7 line

**Goal:** an application can supply its own request fingerprint, a replay never duplicates a response header the app already set, and a success response above `WithMaxBodyCache` reaches the client unchanged while its key stays completed.
**Scope:** `commons/net/http/idempotency/`
**Dependencies:** none
**Done when:** the three tasks below are green, each with a test that failed before its change.
**Status:** Pending

#### Task 1.1.1: An application-supplied request fingerprint

- [x] Done

**Context:** `Middleware.handle` computes `requestFingerprint(c.Method(), c.Path(), c.Body())` unconditionally (`idempotency.go:954`), and `requestFingerprintWithScope` re-reads `c.Body()` for the scoped form. Two consumer facts break on that. (a) An application running Fiber with `StreamRequestBody: true` streams large uploads to its handler; fasthttp's `Body()` on a streamed request copies the whole body into memory and closes the stream, so mounting this middleware on such a route silently buffers up to the route's limit (Matcher: 1 GiB) per request and the handler's `IsBodyStream()` branch is never taken. (b) For `multipart/form-data`, `mime/multipart.NewWriter` and every browser pick a fresh random boundary per request, so a byte-identical logical retry of a file upload never matches its own stored fingerprint and is refused `422 IDEMPOTENCY_KEY_REUSE`; the published "retry with the same key" contract cannot be honoured on any multipart route. Both need the same thing: a way for the application to say what identifies the request, instead of the raw body.

**Implementation vision:** add `type FingerprintProvider func(c fiber.Ctx) ([]byte, error)` and `func WithFingerprintProvider(provider FingerprintProvider) Option`. When set, the middleware fingerprints `method + path + provider bytes` (through the same SHA-256 input builder, and through the scoped variant when a scope provider is also set) and NEVER calls `c.Body()`; a provider error is treated exactly like the store being unavailable before the handler (the fail-closed posture and `WithUnavailableHandler` apply), because a request whose identity cannot be established must not run unprotected. When not set, behaviour is byte-for-byte today's, including the legacy-input compatibility in `requestFingerprintWithScope`. Doc comment on the option: what the provider must return (stable bytes for the same logical request: for multipart, e.g. the declared filenames, sizes and the fields the app considers identity; for streamed bodies, whatever the app can read without consuming the stream), and that the raw body is the default because it is the strictest identity. Tests, RED first: (1) a Fiber app with `StreamRequestBody: true` and a 64 KiB body: without a provider the handler sees a buffered body; with a provider it sees `IsBodyStream() == true` and reads the stream itself (this is the differential that proves the body is untouched); (2) two multipart requests built with fresh boundaries and a provider over the part names and sizes: the second replays the first response and the handler ran once; (3) provider error: no handler call, the unavailable response, no record left in the store; (4) provider set together with `WithFingerprintScopeProvider`: the scope still namespaces the fingerprint. Update `doc.go`'s behaviour list with one paragraph.

**Files:**
- Modify: `commons/net/http/idempotency/idempotency.go`, `commons/net/http/idempotency/doc.go`
- Test: `commons/net/http/idempotency/fingerprint_provider_test.go` (new)

**Verification:** `go test -tags=unit -race -run 'Fingerprint' ./commons/net/http/idempotency/` green, and the differential test (1) fails when the provider path is made to call `c.Body()` (prove once, revert); then the whole package green.

**Done when:** a consumer can mount the middleware on a streamed and on a multipart route with a provider and the four tests above pass; every existing test is unchanged and green.

#### Task 1.1.2: A replay replaces the headers it captured instead of adding to them

- [x] Done

**Context:** `captureResponse` stores every response header except `Content-Type`, `Content-Length`, `Transfer-Encoding` and the replayed marker (`idempotency.go:1355-1365`), and `replay` re-applies them with `c.Response().Header.Add(name, value)` (`idempotency.go:1419-1423`) without clearing first. A consumer that mounts header-setting middleware globally (`cors.New()`, `helmet.New()` via `app.Use`, the common shape) has those headers on the response BEFORE the idempotency middleware runs, and again inside the captured set. A replayed response therefore leaves with two `Access-Control-Allow-Origin`, two `X-Frame-Options`, two `X-Content-Type-Options`, two `Vary`. Browsers refuse a CORS response whose `Access-Control-Allow-Origin` "contains multiple values", so a double-clicked mutation that succeeded is reported to the user as a network failure.

**Implementation vision:** in `replay`, for each captured header name: `c.Response().Header.Del(name)` once, then `Add` each captured value in order. The captured set is the authority for those names; multi-valued captured headers (two `Set-Cookie`, two `Link`) keep all their values; headers the app sets that were NOT captured are untouched. Tests, RED first: (1) an app with `cors.New()` and `helmet.New()` mounted globally and the middleware on a POST route: the replayed response has exactly one value for each of `Access-Control-Allow-Origin`, `X-Frame-Options`, `X-Content-Type-Options`, `Vary` (assert with `resp.Header.Values`); (2) a handler that sets two `Link` values: the replay carries both, in order; (3) a header present on the live response but absent from the capture is still present on the replay.

**Files:**
- Modify: `commons/net/http/idempotency/idempotency.go`
- Test: `commons/net/http/idempotency/replay_headers_test.go` (new)

**Verification:** `go test -tags=unit -race -run 'Replay' ./commons/net/http/idempotency/` green; test (1) fails on the current `Add`-only code (prove, then apply the fix).

**Done when:** a replay never carries a duplicated header, multi-valued captured headers survive, and the three tests are green.

#### Task 1.1.3: A success above the body cap reaches the client, and its key stays completed

- [ ] Done

**Context:** `captureResponse` returns `errResponseTooLarge` when the success body exceeds `maxBodyCache` (`idempotency.go:1342`, default 1 MB), and the caller at `idempotency.go:1151` routes that into `failPostHandler(c, key, processing, record, ttl)`, the same path as a storage failure after the handler ran. Measure what that does to (a) the response the client receives for a mutation that HAS committed, and (b) the key: is it released (a resend re-executes the mutation), left as the processing lease until its TTL (a resend answers the in-flight conflict until then), or fenced. Whatever the answer, a body size is not a fault: the handler succeeded, the client must receive that success unchanged, and a resend under the same key must never execute the mutation a second time.

**Implementation vision:** first write the measurement as a test that asserts the CORRECT behaviour and record its RED output in this task. Then implement: on `errResponseTooLarge` the middleware completes the record with `State = keyStateComplete`, no cached body, and a marker (`ReplayUnavailable: true` or equivalent on the stored record; keep the record format backward-readable, `decodeLegacyRecord` and the existing record tests must still pass) and returns the handler's response untouched. A later request under the same key with the same fingerprint finds a completed record with no replayable body and answers through a new `WithReplayUnavailableHandler(fn fiber.Handler) Option`, default: `409 Conflict` with a problem body stating the operation completed and its response is not replayable (never the in-flight message, never a 500, never a re-execution). A different fingerprint under that key is still the existing key-reuse refusal. Tests, RED first: (1) over-cap success: client gets the handler's status and body; (2) resend, same fingerprint: handler ran once in total, response is the replay-unavailable refusal; (3) resend, different fingerprint: key-reuse refusal; (4) the default handler is replaceable through the option; (5) an under-cap success still replays as before (regression guard). Update `doc.go`.

**Files:**
- Modify: `commons/net/http/idempotency/idempotency.go`, `commons/net/http/idempotency/store.go` (record fields only if needed), `commons/net/http/idempotency/doc.go`
- Test: `commons/net/http/idempotency/oversize_response_test.go` (new)

**Verification:** `go test -tags=unit -race ./commons/net/http/idempotency/...` green including `legacy_record_test.go`; the measurement test's RED output is quoted in this task's Result.

**Done when:** an over-cap success is delivered unchanged, its key cannot re-execute the mutation, the refusal is documented and overridable, and every pre-existing test is unchanged and green.

## Bugs found

| Id | File | Symptom | Fix commit |
|---|---|---|---|

## Bugs found outside this package

| File | Symptom | Evidence |
|---|---|---|
