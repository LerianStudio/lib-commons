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

This lane owns `commons/net/http/idempotency/**` and this file, plus the `commons/net/http/idempotency` bullet of `README.md` (widened by the orchestrator on 2026-09-21 after review round 1). Nothing else.

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
**Status:** Done

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

- [x] Done

**Measured RED** (`go test -tags=unit -run TestOversizeResponse ./commons/net/http/idempotency/`, before the change): the over-cap success answered `503` instead of the handler's `201`, with the body `{"code":503,"title":"IDEMPOTENCY_UNAVAILABLE","message":"request processing finished but its replay response could not be persisted; do not retry with a new key - reconcile the original request first"}`, and `X-Idempotency-Fenced: true`; the resend answered `422 IDEMPOTENCY_OUTCOME_UNRECORDED`. So a mutation that HAD committed was reported to its client as a store failure, and the key was fenced as if the outcome were unknown. **Answer to the measurement:** the key was fenced (not released, not left on the lease), so a resend was refused for the whole retention window - the double-execution was already closed; what was wrong was the document, on both requests.

**Result:** an over-cap success is now delivered unchanged and the record completes with `outcome: "not-replayable"` and no stored body; a resend with the same fingerprint gets `409 IDEMPOTENCY_REPLAY_UNAVAILABLE` (no `Retry-After`, no replay header, overridable through `WithReplayUnavailableHandler`), a resend with a different fingerprint still gets the key-reuse refusal, and the handler runs exactly once. Two pre-existing tests asserted the old 503-and-fence contract and were updated to the new one: `TestCheck_WithMaxBodyCache` (`idempotency_test.go`) and `TestCheck_OversizedResponse_FailsClosedWithoutCompletionMarker`, renamed `..._CompletesWithoutAReplayableReceipt` (`policy_test.go`).

**Context:** `captureResponse` returns `errResponseTooLarge` when the success body exceeds `maxBodyCache` (`idempotency.go:1342`, default 1 MB), and the caller at `idempotency.go:1151` routes that into `failPostHandler(c, key, processing, record, ttl)`, the same path as a storage failure after the handler ran. Measure what that does to (a) the response the client receives for a mutation that HAS committed, and (b) the key: is it released (a resend re-executes the mutation), left as the processing lease until its TTL (a resend answers the in-flight conflict until then), or fenced. Whatever the answer, a body size is not a fault: the handler succeeded, the client must receive that success unchanged, and a resend under the same key must never execute the mutation a second time.

**Implementation vision:** first write the measurement as a test that asserts the CORRECT behaviour and record its RED output in this task. Then implement: on `errResponseTooLarge` the middleware completes the record with `State = keyStateComplete`, no cached body, and a marker (`ReplayUnavailable: true` or equivalent on the stored record; keep the record format backward-readable, `decodeLegacyRecord` and the existing record tests must still pass) and returns the handler's response untouched. A later request under the same key with the same fingerprint finds a completed record with no replayable body and answers through a new `WithReplayUnavailableHandler(fn fiber.Handler) Option`, default: `409 Conflict` with a problem body stating the operation completed and its response is not replayable (never the in-flight message, never a 500, never a re-execution). A different fingerprint under that key is still the existing key-reuse refusal. Tests, RED first: (1) over-cap success: client gets the handler's status and body; (2) resend, same fingerprint: handler ran once in total, response is the replay-unavailable refusal; (3) resend, different fingerprint: key-reuse refusal; (4) the default handler is replaceable through the option; (5) an under-cap success still replays as before (regression guard). Update `doc.go`.

**Files:**
- Modify: `commons/net/http/idempotency/idempotency.go`, `commons/net/http/idempotency/store.go` (record fields only if needed), `commons/net/http/idempotency/doc.go`
- Test: `commons/net/http/idempotency/oversize_response_test.go` (new)

**Verification:** `go test -tags=unit -race ./commons/net/http/idempotency/...` green including `legacy_record_test.go`; the measurement test's RED output is quoted in this task's Result.

**Done when:** an over-cap success is delivered unchanged, its key cannot re-execute the mutation, the refusal is documented and overridable, and every pre-existing test is unchanged and green.
### Epic 1.2: Review round 1 residue (added by the orchestrator, 2026-09-21)

**Goal:** the replay reproduces the handler's response and nothing else; an over-cap 4xx never claims an unknown outcome; the fingerprint provider's time is not charged to the store; the README says what ships.
**Scope:** `commons/net/http/idempotency/**`, the `commons/net/http/idempotency` bullet of `README.md`, this file.
**Dependencies:** Epic 1.1 (landed on this branch).
**Done when:** the package suite is green; the four decisions below are in code with a RED each; the README bullet has no false clause; exactly one `feat(net)` commit exists in the range.
**Status:** Pending

#### Task 1.2.1: A replay applies the handler's header delta, and live headers win elsewhere

- [x] Done

**Context:** Task 1.1.2 made the replay Del-then-Add every captured header name. Reviewers measured (findings F1, F2, F6, F9, F11, F12, F13) that this discards a per-request value set ABOVE the middleware on the duplicate: `X-Request-Id` from a requestid middleware (the duplicate answers with the ORIGINAL request's id), a rotated session or a fresh CSRF cookie minted by `app.Use` above (the cookie NAME is in the capture, so `DelCookie` removes the fresh one and re-adds the stale one). `TestReplay_LiveCookieFromOtherMiddleware_Survives` mints the same value on both requests, so it cannot see the overwrite. `captureResponse` (`idempotency.go:1516`) captures EVERY response header except Content-Type, Content-Length, Transfer-Encoding and X-Idempotency-Replayed.

**Decision (orchestrator):** the capture is the HANDLER's contribution, not the whole response. Snapshot the response headers immediately before `c.Next()` (name → value list; cookies keyed by cookie name). After the handler, capture only the names whose value list differs from the snapshot (added or changed) and, for Set-Cookie, only the cookie names added or changed. On replay, for each captured name: replace it (Del, or DelCookie per captured cookie name, then Add each value); every other live header — set by middleware above on THIS request — stays. Write the consequence table into doc.go: X-Request-Id, CORS, helmet, CSRF and session rotation from above → live on the replay; Location, ETag, Cache-Control and a Set-Cookie minted by the handler → replayed byte-identical; a header helmet sets above and the handler overrides (`Cache-Control: no-store`) → captured and replayed, overriding the live one, because that is what the original response carried. Mixed versions: a record captured by an older version holds the full header set and replays under this version with replace semantics, exactly as Task 1.1.2 shipped; it self-heals within retention. State that in doc.go's mixed-version list. Keep the existing exclusions (Content-Type stays a separate field).

**Tests, RED first, each with DIFFERENT values on the two requests so the assertion can fail:** an X-Request-Id minted above differs per request and the replay carries the second; a csrf cookie minted above with a new value per request survives the replay with the second value; a Location header and a session cookie set by the HANDLER replay byte-identical; a header helmet sets above and the handler overrides replays with the handler's value; an old-format full capture (record built by hand) replays with replace semantics; `TestReplay_LiveCookieFromOtherMiddleware_Survives` rewritten to mint distinct values.

**Files:**
- Modify: `commons/net/http/idempotency/idempotency.go`, `commons/net/http/idempotency/doc.go`, `commons/net/http/idempotency/replay_headers_test.go`, this file

**Verification:** `go test -tags=unit -count=1 ./commons/net/http/idempotency/...` green; `go vet -tags=unit ./commons/net/http/idempotency/...` exits 0; the repo's lint target scoped to the package if the Makefile offers one (read it).

**Done when:** the five scenarios above pass, the doc table exists, and the capture holds only the handler's delta.

#### Task 1.2.2: Over-cap 4xx releases the key; the provider runs before the store deadline; the fourth refusal code is exported

- [x] Done

**Measured RED** (before each change, `go test -tags=unit -count=1 -run <name> ./commons/net/http/idempotency/`):

- (a) `TestOversizeResponse_ClientErrorNeverClaimsSuccess`, rewritten to the decision: `expected: 2, actual: 1` on the handler counter, and the resend's body was `{"code":422,"title":"IDEMPOTENCY_OUTCOME_UNRECORDED","message":"an earlier request with this idempotency key ran without recording its outcome; do not retry with a new key - reconcile the original request first"}` where the handler's own rejection document was due. A validation route whose report exceeds the cap held its key for the whole retention window and sent every resend to reconcile a mutation that had committed nothing.
- (b) `TestFingerprintProvider_SlowProviderIsNotChargedToTheStoreDeadline`: `expected: 1, actual: 2` on the handler counter and an empty `X-Idempotency-Replayed`. A provider sleeping 150 ms against a 50 ms `WithRedisTimeout` and a healthy miniredis timed out the first store call, and the fail-open default ran the mutation UNPROTECTED - no key was ever held, so the duplicate executed again.
- (c) `TestReplayUnavailable_BypassesTheOtherRefusalSeams` passes on the shipped code, so it was proven to bite: routing `respondReplayUnavailable` through `onPostHandlerUnavailable` made it fail with `expected: 409, actual: 503` and the seam's own error line. Mutation reverted.
- (d) `grep -n '"IDEMPOTENCY_REPLAY_UNAVAILABLE"' commons/net/http/idempotency/*.go` printed four sites (the response body, two doc comments, and the doc.go branch list) plus the test literals; it now prints only the const line.

**Result:** an over-cap 4xx releases the key and its resend re-runs the handler and collects the same rejection (`outcomeUnrecorded` is gone from that arm; `outcomeNotReplayable` is 2xx-only). The store deadline opens after the fingerprint and TTL providers run, so application I/O is no longer charged to `WithRedisTimeout`. `RefusalCodeReplayUnavailable` is exported and is the branch's only `feat(net)` commit. `WithFingerprintProvider` and doc.go's mixed-version section both warn that enabling the provider changes the digest of the same logical request. The three release sites share one `releaseOwned` helper; the existing log messages are unchanged.

**Context:** (a) F4/F8 — `idempotency.go:1333`: an over-cap 4xx completes the record with `outcomeUnrecorded` through `store.Complete`, bypassing `markOutcomeUnknown`: no `X-Idempotency-Fenced` header, and a resend is routed to `WithPostHandlerUnavailableHandler` ("committed or unknown, reconcile") for a request that committed nothing. (b) F5 — `idempotency.go:1076-1079`: `context.WithTimeout(c.Context(), m.redisTimeout)` is created BEFORE `resolveFingerprint` runs, so a provider that reads a multipart body is charged against the store's 500 ms deadline; a slow provider times out the first store call and, under the fail-open default, the mutation runs unprotected. (c) F14 — nothing tests that `respondReplayUnavailable` bypasses `WithPostHandlerUnavailableHandler` and `WithTerminalRefusalHandler`. (d) F16 — `"IDEMPOTENCY_REPLAY_UNAVAILABLE"` is a bare literal while the other three refusal codes are exported constants (`idempotency.go:176-184`). (e) F17 — neither `WithFingerprintProvider`'s doc nor doc.go warns that enabling the provider changes the digest of the same logical request, so a retry across a rolling deploy is refused as key reuse.

**Decision (orchestrator):** (a) an over-cap 4xx RELEASES the key, exactly as `ClientErrorPolicyRelease` would, logged at INFO with status and size; the client receives its rejection unchanged; a resend re-executes the handler, which answers the same rejection. Nothing about it is "unrecorded". Delete the `outcomeUnrecorded` assignment on that arm; `outcomeNotReplayable` stays for 2xx only. RED: rewrite `TestOversizeResponse_ClientErrorNeverClaimsSuccess` to assert the resend runs the handler again (handler counter 2), no fence header, no 409. (b) resolve fingerprint and TTL BEFORE `context.WithTimeout`; RED: a provider that sleeps longer than `redisTimeout` against a healthy in-memory store must NOT reach the store-error path (handler ran once, key held afterwards). (c) one test with both seams wired to `t.Fatal` if called, asserting the built-in 409 body. (d) `RefusalCodeReplayUnavailable = "IDEMPOTENCY_REPLAY_UNAVAILABLE"` in the const block with a doc line, the literal replaced. It is a new exported symbol: commit it alone as `feat(net): export the replay-unavailable refusal code` — the only `feat` in the range, so semantic-release cuts a MINOR for a branch that adds three options (`.releaserc.yml` maps `fix` → patch, `feat` → minor; finding F10). (e) one paragraph on the option's doc and one bullet in doc.go's mixed-version list: enable the provider with a retention-TTL gap, or accept one retention window of `IDEMPOTENCY_KEY_REUSE` on retries that straddle the deploy.

**Files:**
- Modify: `commons/net/http/idempotency/idempotency.go`, `commons/net/http/idempotency/doc.go`, `commons/net/http/idempotency/oversize_response_test.go`, `commons/net/http/idempotency/fingerprint_provider_test.go`, this file
- Create: `commons/net/http/idempotency/replay_unavailable_test.go`

**Verification:** package tests green; `grep -n '"IDEMPOTENCY_REPLAY_UNAVAILABLE"' commons/net/http/idempotency/*.go` prints only the const line; `git log --oneline origin/develop..HEAD | grep -c ' feat(net)'` prints 1.

**Done when:** the four decisions are in code with their RED recorded; the feat commit exists.

#### Task 1.2.3: The README bullet says what ships

- [ ] Done

**Context:** F3/F7/F15 — `README.md:67`, the `commons/net/http/idempotency` bullet, still says an uncapturable response fails closed with 503 and fences the key; omits `WithFingerprintProvider`, `WithReplayUnavailableHandler` and `IDEMPOTENCY_REPLAY_UNAVAILABLE`; the previous carve-out excluded README, so the lane recorded the contradiction instead of fixing it. The carve-out is widened (see `## Ownership carve-outs`).

**Implementation vision:** rewrite only the clauses that are false after Epics 1.1 and 1.2, in the bullet's existing register (one long bullet; do not restructure it): an over-cap 2xx completes without a receipt and its resend is 409 `IDEMPOTENCY_REPLAY_UNAVAILABLE` via `WithReplayUnavailableHandler`, never through the post-handler seam; an over-cap 4xx releases; the fingerprint may come from `WithFingerprintProvider`; a replay applies the handler's header delta and preserves live headers; the fourth code joins the rejection-handler list and the refusal-code list with its exported name. Then the record: append the Epic 1.2 correction under Task 1.1.2's write-back ("replaces the headers it captured" is now "replaces the handler's delta"), and mark the README row in `## Bugs found outside this package` as fixed here.

**Files:**
- Modify: `README.md` (that bullet only), this file

**Verification:** `grep -o 'WithFingerprintProvider\|WithReplayUnavailableHandler\|IDEMPOTENCY_REPLAY_UNAVAILABLE' README.md | sort -u | wc -l` prints 3; `grep -n 'cannot be captured' README.md` prints nothing.

**Done when:** no clause of the bullet contradicts the package; the record carries the correction.


## Bugs found

| Id | File | Symptom | Fix commit |
|---|---|---|---|
| B1 | `commons/net/http/idempotency/idempotency.go` | An over-cap response completed the key as carrying no replayable receipt whatever its status, so a 4xx cached under the default client-error policy answered a resend `409 IDEMPOTENCY_REPLAY_UNAVAILABLE`: "already completed successfully". The mutation was REJECTED and committed nothing, and the rejection document was unreachable for the whole retention window. A non-2xx now completes with the unrecorded outcome, whose refusal asserts nothing. **Superseded by Task 1.2.2:** the unrecorded outcome still asserted something false ("reconcile the original request") about a rejection that committed nothing, so an over-cap 4xx now RELEASES the key instead of completing it at all. | `f22a637`, `ae90297` |
| B2 | `commons/net/http/idempotency/idempotency.go` | A `WithResponseCodec` producing zero bytes returned the same error as an over-cap body, so a codec malfunction silently completed every response on the route as non-replayable while the WARN log and the client document blamed `WithMaxBodyCache`, which cannot fix it. An empty encoding now has its own error and keeps the loud post-handler failure. | `f22a637` |
| B3 | `commons/net/http/idempotency/idempotency.go` | Task 1.1.2's `Header.Del(name)` hits fasthttp's special case for `Set-Cookie` and empties the WHOLE cookie jar, so a replay discarded a cookie another middleware had minted on that request — a rotated session, a fresh CSRF token — and handed back the captured one, whose next mutation the CSRF check refuses. Captured cookies are now cleared one cookie name at a time through `DelCookie`. | `f22a637` |
| B4 | `commons/net/http/idempotency/fingerprint_provider_test.go` | Task 1.1.1 shipped with no test proving the provider's bytes reach the digest: dropping the identity from `resolveFingerprint` left the whole package green. A client uploading `june.csv` then `july.csv` under one key would have received the first upload's receipt without the handler running. Closed by `TestFingerprintProvider_DifferentIdentityIsReuse`. | `9d29271` |

## Bugs found outside this package

| File | Symptom | Evidence |
|---|---|---|
| `README.md` (the `commons/net/http/idempotency` bullet) | The repo's canonical contract for this middleware contradicts the shipped behaviour in four places after this lane: it still says an exact response that "cannot be captured" fails closed with 503 `IDEMPOTENCY_UNAVAILABLE` and that response-capture failure "additionally fences the key", both now false for an over-cap body; its exhaustive list of customizable rejection bodies omits `WithReplayUnavailableHandler`; its duplicate-outcome list omits `IDEMPOTENCY_REPLAY_UNAVAILABLE`; and its fingerprint paragraph names only `WithKeyProvider` and `WithFingerprintScopeProvider`, with `WithFingerprintProvider` absent. An integrator wiring from the README wires `WithPostHandlerUnavailableHandler` expecting it to own the over-cap resend and ships the library's raw 409 envelope instead; a consumer with a multipart or streamed-upload route never learns the option that unblocks it. | `README.md:67`, read against `commons/net/http/idempotency/doc.go` and `idempotency.go` on this branch. The file is outside this lane's carve-out (`commons/net/http/idempotency/**` and this plan), so it is recorded rather than edited — a lane-scoping gap, not an implementer slip. |
