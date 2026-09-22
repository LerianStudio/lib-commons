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
| 1 | a consumer with streamed or multipart requests, global response-header middleware, or success bodies above the cache cap can mount the middleware without re-executing a mutation or duplicating headers | 1.1, 1.2, 1.3, 1.4 | Complete |

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

**Corrected by Task 1.2.1 (Epic 1.2):** this task's title and vision are now one word wrong. The replay does not replace "the headers it captured" — it replaces the HANDLER'S DELTA. Capturing the whole response set meant the replace semantics landed on per-request values set ABOVE the middleware too, so a duplicate answered with the original request's correlation id and with a stale CSRF cookie in place of the fresh one another middleware had just minted. The capture is now the delta against a snapshot taken immediately before the handler runs; everything else on the response stays live. Read this task as "a replay replaces the handler's delta instead of adding to the live response".

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
**Status:** Done

#### Task 1.2.1: A replay applies the handler's header delta, and live headers win elsewhere

- [x] Done

**Context:** Task 1.1.2 made the replay Del-then-Add every captured header name. Reviewers measured (findings F1, F2, F6, F9, F11, F12, F13) that this discards a per-request value set ABOVE the middleware on the duplicate: `X-Request-Id` from a requestid middleware (the duplicate answers with the ORIGINAL request's id), a rotated session or a fresh CSRF cookie minted by `app.Use` above (the cookie NAME is in the capture, so `DelCookie` removes the fresh one and re-adds the stale one). `TestReplay_LiveCookieFromOtherMiddleware_Survives` mints the same value on both requests, so it cannot see the overwrite. `captureResponse` (`idempotency.go:1516`) captures EVERY response header except Content-Type, Content-Length, Transfer-Encoding and X-Idempotency-Replayed.

**Decision (orchestrator):** the capture is the HANDLER's contribution, not the whole response. Snapshot the response headers immediately before `c.Next()` (name → value list; cookies keyed by cookie name). After the handler, capture only the names whose value list differs from the snapshot (added or changed) and, for Set-Cookie, only the cookie names added or changed. On replay, for each captured name: replace it (Del, or DelCookie per captured cookie name, then Add each value); every other live header — set by middleware above on THIS request — stays. Write the consequence table into doc.go: X-Request-Id, CORS, helmet, CSRF and session rotation from above → live on the replay; Location, ETag, Cache-Control and a Set-Cookie minted by the handler → replayed byte-identical; a header helmet sets above and the handler overrides (`Cache-Control: no-store`) → captured and replayed, overriding the live one, because that is what the original response carried. Mixed versions: a record captured by an older version holds the full header set and replays under this version with replace semantics, exactly as Task 1.1.2 shipped; it self-heals within retention. State that in doc.go's mixed-version list. Keep the existing exclusions (Content-Type stays a separate field).

**Tests, RED first, each with DIFFERENT values on the two requests so the assertion can fail:** an X-Request-Id minted above differs per request and the replay carries the second; a csrf cookie minted above with a new value per request survives the replay with the second value; a Location header and a session cookie set by the HANDLER replay byte-identical; a header helmet sets above and the handler overrides replays with the handler's value; an old-format full capture (record built by hand) replays with replace semantics; `TestReplay_LiveCookieFromOtherMiddleware_Survives` rewritten to mint distinct values.

**Files:**
- Modify: `commons/net/http/idempotency/idempotency.go`, `commons/net/http/idempotency/doc.go`, `commons/net/http/idempotency/replay_headers_test.go`, this file

**Verification:** `go test -tags=unit -count=1 ./commons/net/http/idempotency/...` green; `go vet -tags=unit ./commons/net/http/idempotency/...` exits 0; the repo's lint target scoped to the package if the Makefile offers one (read it).

**Done when:** the five scenarios above pass, the doc table exists, and the capture holds only the handler's delta.

#### Task 1.2.2: Over-cap 4xx releases the key (decision (a), reversed 2026-09-21 by `d058590`); the provider runs before the store deadline; the fourth refusal code is exported

- [x] Done

**Measured RED** (before each change, `go test -tags=unit -count=1 -run <name> ./commons/net/http/idempotency/`):

- (a) `TestOversizeResponse_ClientErrorNeverClaimsSuccess`, rewritten to the decision: `expected: 2, actual: 1` on the handler counter, and the resend's body was `{"code":422,"title":"IDEMPOTENCY_OUTCOME_UNRECORDED","message":"an earlier request with this idempotency key ran without recording its outcome; do not retry with a new key - reconcile the original request first"}` where the handler's own rejection document was due. A validation route whose report exceeds the cap held its key for the whole retention window and sent every resend to reconcile a mutation that had committed nothing.
- (b) `TestFingerprintProvider_SlowProviderIsNotChargedToTheStoreDeadline`: `expected: 1, actual: 2` on the handler counter and an empty `X-Idempotency-Replayed`. A provider sleeping 150 ms against a 50 ms `WithRedisTimeout` and a healthy miniredis timed out the first store call, and the fail-open default ran the mutation UNPROTECTED - no key was ever held, so the duplicate executed again.
- (c) `TestReplayUnavailable_BypassesTheOtherRefusalSeams` passes on the shipped code, so it was proven to bite: routing `respondReplayUnavailable` through `onPostHandlerUnavailable` made it fail with `expected: 409, actual: 503` and the seam's own error line. Mutation reverted.
- (d) `grep -n '"IDEMPOTENCY_REPLAY_UNAVAILABLE"' commons/net/http/idempotency/*.go` printed four sites (the response body, two doc comments, and the doc.go branch list) plus the test literals; it now prints only the const line.

**Result** — *reversed 2026-09-21 by `d058590`, decision (a) only: an over-cap response of ANY status is delivered unchanged and completes its key with no receipt (`outcomeNotReplayable` covers an over-cap success and an over-cap rejection held by `ClientErrorPolicyCache`), its resend is refused 409 `IDEMPOTENCY_REPLAY_UNAVAILABLE`, `ClientErrorPolicyRelease` still releases every 4xx before the response is captured, and the release sites are two, not three; the paragraph below is kept as it was written on the day:* an over-cap 4xx releases the key and its resend re-runs the handler and collects the same rejection (`outcomeUnrecorded` is gone from that arm; `outcomeNotReplayable` is 2xx-only). The store deadline opens after the fingerprint and TTL providers run, so application I/O is no longer charged to `WithRedisTimeout`. `RefusalCodeReplayUnavailable` is exported and is the branch's only `feat(net)` commit. `WithFingerprintProvider` and doc.go's mixed-version section both warn that enabling the provider changes the digest of the same logical request. The three release sites share one `releaseOwned` helper; the existing log messages are unchanged.

**Context:** (a) F4/F8 — `idempotency.go:1333`: an over-cap 4xx completes the record with `outcomeUnrecorded` through `store.Complete`, bypassing `markOutcomeUnknown`: no `X-Idempotency-Fenced` header, and a resend is routed to `WithPostHandlerUnavailableHandler` ("committed or unknown, reconcile") for a request that committed nothing. (b) F5 — `idempotency.go:1076-1079`: `context.WithTimeout(c.Context(), m.redisTimeout)` is created BEFORE `resolveFingerprint` runs, so a provider that reads a multipart body is charged against the store's 500 ms deadline; a slow provider times out the first store call and, under the fail-open default, the mutation runs unprotected. (c) F14 — nothing tests that `respondReplayUnavailable` bypasses `WithPostHandlerUnavailableHandler` and `WithTerminalRefusalHandler`. (d) F16 — `"IDEMPOTENCY_REPLAY_UNAVAILABLE"` is a bare literal while the other three refusal codes are exported constants (`idempotency.go:176-184`). (e) F17 — neither `WithFingerprintProvider`'s doc nor doc.go warns that enabling the provider changes the digest of the same logical request, so a retry across a rolling deploy is refused as key reuse.

**Decision (orchestrator):** (a) an over-cap 4xx RELEASES the key, exactly as `ClientErrorPolicyRelease` would, logged at INFO with status and size; the client receives its rejection unchanged; a resend re-executes the handler, which answers the same rejection. Nothing about it is "unrecorded". Delete the `outcomeUnrecorded` assignment on that arm; `outcomeNotReplayable` stays for 2xx only. RED: rewrite `TestOversizeResponse_ClientErrorNeverClaimsSuccess` to assert the resend runs the handler again (handler counter 2), no fence header, no 409. (b) resolve fingerprint and TTL BEFORE `context.WithTimeout`; RED: a provider that sleeps longer than `redisTimeout` against a healthy in-memory store must NOT reach the store-error path (handler ran once, key held afterwards). (c) one test with both seams wired to `t.Fatal` if called, asserting the built-in 409 body. (d) `RefusalCodeReplayUnavailable = "IDEMPOTENCY_REPLAY_UNAVAILABLE"` in the const block with a doc line, the literal replaced. It is a new exported symbol: commit it alone as `feat(net): export the replay-unavailable refusal code` — the only `feat` in the range, so semantic-release cuts a MINOR for a branch that adds three options (`.releaserc.yml` maps `fix` → patch, `feat` → minor; finding F10). (e) one paragraph on the option's doc and one bullet in doc.go's mixed-version list: enable the provider with a retention-TTL gap, or accept one retention window of `IDEMPOTENCY_KEY_REUSE` on retries that straddle the deploy.

**Corrected by the self-heal pass (2026-09-21), decision (a) only:** the over-cap 4xx no longer releases. Three reviewers measured the same defect independently: `ClientErrorPolicyRelease` already releases every 4xx BEFORE the response is captured, so the release arm added here was reachable only under `ClientErrorPolicyCache` and overrode it 100% of the time. The length of a rejection document decided whether a rejection path could re-execute — the same route re-running its 4xx side effects (a declined-attempt audit row, a quota decrement, a fraud counter) for a long validation report and not for a short one, against an explicit configured policy the middleware cannot second-guess (the reasoning `WithServerErrorPolicy`'s own godoc gives for leaving that call to the route owner). An over-cap response now completes identically whatever its status: delivered unchanged, key held, `outcomeNotReplayable`, resend refused 409 `RefusalCodeReplayUnavailable`. The wording objection that motivated the release was a DOCUMENT problem and is fixed as one — the 409 no longer claims the original request "already completed successfully", it says it "already ran and its response was delivered", which is true of a success and of a rejection. A route that wants its rejection re-derived sets `ClientErrorPolicyRelease`, which is what that option is for. Decisions (b), (c), (d) and (e) stand. Two consequences for the text above: `TestOversizeResponse_ClientErrorNeverClaimsSuccess` is now `TestOversizeResponse_ClientErrorHonoursTheCachePolicy` (plus `..._ClientErrorReleasePolicyStillReleases` for the other half), and the release sites are two, not three.

**Files:**
- Modify: `commons/net/http/idempotency/idempotency.go`, `commons/net/http/idempotency/doc.go`, `commons/net/http/idempotency/oversize_response_test.go`, `commons/net/http/idempotency/fingerprint_provider_test.go`, this file
- Create: `commons/net/http/idempotency/replay_unavailable_test.go`

**Verification:** package tests green; `grep -n '"IDEMPOTENCY_REPLAY_UNAVAILABLE"' commons/net/http/idempotency/*.go` prints only the const line; `git log --oneline origin/develop..HEAD | grep -c ' feat(net)'` prints 1.

**Done when:** the four decisions are in code with their RED recorded; the feat commit exists.

#### Task 1.2.3: The README bullet says what ships

- [x] Done

**Result** — *reversed 2026-09-21 by `d058590`: the README no longer says an over-cap 4xx is RELEASED. At HEAD its over-cap clause says a response above `WithMaxBodyCache` is delivered unchanged whatever its status and completes its key with no receipt, the resend is refused 409 `IDEMPOTENCY_REPLAY_UNAVAILABLE`, and `ClientErrorPolicyRelease` is the one knob that releases a 4xx, applied before capture. The paragraph below is kept as it was written on the day:* the bullet's false clauses are rewritten in place (one line, one bullet, same register). "cannot be captured" is gone from the fail-closed clause: a size is not a capture fault, so an over-cap response takes its own two branches — a 2xx delivered unchanged and completed with no receipt, a 4xx delivered unchanged with the key RELEASED — and neither 503s nor fences. The duplicate-outcome list gains 409 `IDEMPOTENCY_REPLAY_UNAVAILABLE` through `WithReplayUnavailableHandler` alone, the replay clause now says it re-applies the HANDLER's header delta (captured names replaced, not appended) while headers set above on this request stay live, the fingerprint clause names `WithFingerprintProvider` with its pre-handler 503 and its digest-change warning, `WithReplayUnavailableHandler` joins the rejection-handler list, and `RefusalCodeReplayUnavailable` joins the refusal-code list as the fourth code, deliberately outside the terminal trio.

**Context:** F3/F7/F15 — `README.md:67`, the `commons/net/http/idempotency` bullet, still says an uncapturable response fails closed with 503 and fences the key; omits `WithFingerprintProvider`, `WithReplayUnavailableHandler` and `IDEMPOTENCY_REPLAY_UNAVAILABLE`; the previous carve-out excluded README, so the lane recorded the contradiction instead of fixing it. The carve-out is widened (see `## Ownership carve-outs`).

**Implementation vision:** rewrite only the clauses that are false after Epics 1.1 and 1.2, in the bullet's existing register (one long bullet; do not restructure it): an over-cap 2xx completes without a receipt and its resend is 409 `IDEMPOTENCY_REPLAY_UNAVAILABLE` via `WithReplayUnavailableHandler`, never through the post-handler seam; an over-cap 4xx releases (reversed 2026-09-21 by `d058590`: it completes with no receipt like any other over-cap response, unless `ClientErrorPolicyRelease` released it before capture); the fingerprint may come from `WithFingerprintProvider`; a replay applies the handler's header delta and preserves live headers; the fourth code joins the rejection-handler list and the refusal-code list with its exported name. Then the record: append the Epic 1.2 correction under Task 1.1.2's write-back ("replaces the headers it captured" is now "replaces the handler's delta"), and mark the README row in `## Bugs found outside this package` as fixed here.

**Files:**
- Modify: `README.md` (that bullet only), this file

**Verification:** `grep -o 'WithFingerprintProvider\|WithReplayUnavailableHandler\|IDEMPOTENCY_REPLAY_UNAVAILABLE' README.md | sort -u | wc -l` prints 3; `grep -n 'cannot be captured' README.md` prints nothing.

**Done when:** no clause of the bullet contradicts the package; the record carries the correction.


### Epic 1.3: Review round 2 residue (added by the orchestrator, 2026-09-21)

**Goal:** every comment, doc table and record in this branch states the behaviour that ships after the round-2 self-heal (`d058590`, `a14b4cc`, `408486a`), the one test that went vacuous when the capture narrowed to a delta bites again, and an over-cap completion is traceable from its WARN line to the 409 it will cause.
**Scope:** `commons/net/http/idempotency/{store.go,doc.go,idempotency.go,replay_headers_test.go}`, `README.md:67`, this plan, and — carve-out widened by the orchestrator 2026-09-21 — the `net/http/idempotency` rows of `docs/PROJECT_RULES.md` (nothing else in that file).
**Dependencies:** Epic 1.2 (landed).
**Done when:** `go test -tags=unit -race -count=1 ./commons/net/http/idempotency/...` green; `go vet -tags=unit ./commons/net/http/...` clean; the two tasks ticked; no sentence in the tree says an over-cap 4xx releases the key.
**Status:** Done

**Orchestrator ruling recorded here (2026-09-21):** decision (a) of Task 1.2.2 — "an over-cap 4xx releases the key whatever the policy" — is REVERSED and the self-heal's `d058590` stands. `WithClientErrorPolicy` is the one knob that decides whether a rejection may re-execute; a size arm that overrode it under the default `ClientErrorPolicyCache` was the library second-guessing the application. An over-cap response of any status is delivered unchanged and completes its key with no receipt; a resend is refused 409 `IDEMPOTENCY_REPLAY_UNAVAILABLE`; `ClientErrorPolicyRelease` still releases every 4xx before capture. The consumer chooses: Matcher's lib-swap branch decides its policy with Fred.

#### Task 1.3.1: The code says what it does, and the global-middleware test bites again

- [x] Done

**Measured RED (e)** (`go test -tags=unit -count=1 -run TestReplay_GlobalHeaderMiddleware_NoDuplicatedHeaders ./commons/net/http/idempotency/`, with the `clearCapturedHeader` call deleted from the replay loop):

```
--- FAIL: TestReplay_GlobalHeaderMiddleware_NoDuplicatedHeaders (0.01s)
    replay_headers_test.go:103:
        Error:      Not equal:
                    expected: []string{"ALLOWALL"}
                    actual  : []string{"SAMEORIGIN", "ALLOWALL"}
        Messages:   a captured name the middleware above sets again must be REPLACED on the replay, not appended to
```

The same mutation against the test as it stood at `f8188a5` printed `ok` — measured, not assumed: cors and helmet write before `c.Next()`, so that app's capture was empty and the replay's header loop never ran. The handler now overrides helmet's `X-Frame-Options`, which puts one name in the delta that the live middleware also sets on the duplicate; the cors and helmet names it does not touch are asserted to stay live at one value each, which is where the "no duplicate" claim really lives now.

**Measured RED (d)** (`grep -n -A5 'replay response exceeds the configured limit\|exceeds maxBodyCache' commons/net/http/idempotency/idempotency.go`): the completion WARN carried `error` and `status_code`, its companion in `captureResponse` carried `body_size` and `max_body_cache`, and neither named the tenant or the key — for the one branch that makes a key unreplayable for its whole retention.

**Result:** (a) `outcomeNotReplayable` now reads "a request whose response was DELIVERED but exceeded the body cap — a success, or a rejection the client-error policy chose to hold". (b) doc.go's mixed-version paragraph no longer claims a full-capture record replayed with replace semantics under the version that wrote it: that version added blindly and could duplicate a live header, so the upgrade removes the duplicate and not the staleness. (c) one sentence before the consequence table defines "above" as mounted before `Middleware.Check`, and says everything written during `c.Next()` is the handler's, whoever wrote it — a middleware mounted below `Check` included. (d) both WARN lines gained `idempotency_key_digest` and `tenant_id`, the pair the fence logs already use; `captureResponse` takes the key for that line alone. (e) above.

**Context:** four sentences and one test fell behind the self-heal. (a) `store.go:28-33` says `outcomeNotReplayable` "marks a request that SUCCEEDED"; since `d058590` the same mark is stamped on an over-cap 4xx held under `ClientErrorPolicyCache` (`idempotency.go:~1372`) (F3/F9). (b) `doc.go:~364-370` says a record written by a full-capture version "replays under this version exactly as it did under the one that wrote it: replace semantics"; the only released version that writes full captures replays with a bare `Header.Add` loop (`origin/develop` `idempotency.go:1419-1423`) — replace semantics were never released (F4). (c) `doc.go:~228-240`, the consequence table, promises a per-request header stays live without the precondition that makes it true: the snapshot is taken at `idempotency.go:~1309` immediately before `c.Next()`, so only middleware mounted ABOVE `m.Check()` is "above"; anything written during `c.Next()`, including middleware mounted below `Check()`, lands in the handler delta (F8). (d) the over-cap WARN at `idempotency.go:~1367` logs `error` and `status_code`, and the companion line in `captureResponse` (~`:1571`) logs `body_size` and `max_body_cache`; neither names the tenant or the key, although the branch makes that key permanently unreplayable for its retention (F6). (e) `TestReplay_GlobalHeaderMiddleware_NoDuplicatedHeaders` (`replay_headers_test.go:~52-70`): cors and helmet set their headers before `c.Next()`, so after Task 1.2.1 that app's capture is EMPTY, the replay's header loop never runs, the 8-line comment ("also inside the captured set") is false, and deleting `clearCapturedHeader` leaves the test green (F2/F5).

**Implementation vision:** (a) rewrite the `Outcome` comment: `outcomeNotReplayable` marks a request whose response was delivered but exceeded the body cap — a success, or a rejection the client-error policy chose to hold — so the completion is real and only the receipt is missing. (b) rewrite the mixed-version sentence: such a record replays under this version with replace semantics over the names it holds, where the version that wrote it added blindly and could duplicate a live header; the duplicate still receives the original request's correlation id and captured CSRF token until the record expires. (c) add one sentence before the table: "above" means mounted before `m.Check()`; everything written during `c.Next()` is the handler's, whoever wrote it. (d) both WARN lines gain the fields the rest of the file already uses to identify a record (`grep -n '"key"\|key_digest\|tenant' idempotency.go` and reuse the existing name; if none exists, log `tenant_id` and `key_digest` = the first 16 hex of SHA-256 over the storage key, never the raw client key). (e) rewrite the test so its handler ALSO sets one header helmet already set (e.g. `X-Frame-Options: ALLOWALL`) — that name now enters the delta — and assert the replay carries exactly one value of it (the handler's) while the untouched cors/helmet names stay live with one value each; rewrite the comment to that mechanism. RED first: with `clearCapturedHeader` removed, the replay carries two `X-Frame-Options` values and the test fails; restore, green.

**Files:**
- Modify: `commons/net/http/idempotency/store.go:28-33`
- Modify: `commons/net/http/idempotency/doc.go` (the two passages)
- Modify: `commons/net/http/idempotency/idempotency.go` (the two WARN lines)
- Modify: `commons/net/http/idempotency/replay_headers_test.go`

**Verification:** `go test -tags=unit -race -count=1 ./commons/net/http/idempotency/...` green; the RED excerpt for (e) recorded below this task.

**Done when:** the four passages are true at HEAD and the global-middleware test fails when the clear step is deleted.

#### Task 1.3.2: The records say what shipped

- [x] Done

**Result:** the three stale passages carry a dated reversal clause AHEAD of the sentence they correct, so a reader meets the shipped behaviour first: Task 1.2.2's heading and Result, Task 1.2.3's Result and its implementation vision (a fourth hit, `an over-cap 4xx releases`, found by the verification grep and not listed in the Context), and bug row B1, whose fix-commit column gains `d058590`. `README.md:67` did NOT still say RELEASED — `d058590` rewrote the two over-cap clauses when it landed, so the condition in the Context did not fire; one stale artefact of that rewrite was fixed instead, the fail-closed clause promising "the two branches described further down" when the size branch is now one (a 4xx under `ClientErrorPolicyRelease` never reaches the capture at all). `docs/PROJECT_RULES.md`'s replay row is rewritten whole: capture is now marshalling in the 503 list, a response above `WithMaxBodyCache` is named as a non-fault with its 409 through `WithReplayUnavailableHandler` alone, all four exported refusal codes are listed with the trio/fourth split, and the option list gains `WithFingerprintProvider`, `WithMaxBodyCache` and the clause making `WithClientErrorPolicy` the one knob deciding re-execution. No other row of that file was touched. Two hits of the verification grep are left standing deliberately: `docs/PROJECT_RULES.md:576` and the README's `handler failure/5xx releases the key by default` clause both describe the HANDLER-ERROR release, which is true and is not the over-cap claim; 576 is also explicitly out of this task's reach.

**Context:** three places in this plan still carry decision (a) as landed: Task 1.2.2's Result paragraph (`:~133`: over-cap 4xx releases; `outcomeNotReplayable` 2xx-only; three release sites share `releaseOwned`), Task 1.2.3's Result (`:~153`: README rewritten to "RELEASED"), and bug row B1 (`:~171`, ends "so an over-cap 4xx now RELEASES the key"); only the self-heal note and row B5 carry the correction (F1/F7/F10). `README.md:67` must be read at HEAD and made to state the shipped behaviour if `38a3ebc` (written before the reversal) still says "RELEASES". `docs/PROJECT_RULES.md`'s `net/http/idempotency` row beginning "Exact replay is mandatory after a handler succeeds" says every capture fault returns 503 `IDEMPOTENCY_UNAVAILABLE` and lists options without `WithFingerprintProvider`, `WithReplayUnavailableHandler`, `WithMaxBodyCache`, `WithClientErrorPolicy` or the fourth refusal code (round-2 finding 2, outside the old carve-out; carve-out widened to these rows only).

**Implementation vision:** in the plan, correct each of the three passages IN PLACE with a dated clause ("reversed 2026-09-21 by `d058590`: …"), so the lowest-numbered row a reader finds is already right; do not move the correction elsewhere. README:67: the over-cap clause reads "an over-cap response of any status is delivered unchanged and completes its key with no receipt; the resend is refused 409 `IDEMPOTENCY_REPLAY_UNAVAILABLE`; `ClientErrorPolicyRelease` still releases every 4xx before capture". PROJECT_RULES: rewrite that one row to the same truth — marshalling, codec, completion, stale-owner, missing-response and decode faults return 503 `IDEMPOTENCY_UNAVAILABLE`; a response above `WithMaxBodyCache` is not a fault; the four refusal codes; the options list complete — and touch no other row. Close the "## Bugs found outside this package" row for PROJECT_RULES as fixed by this task.

**Files:**
- Modify: `docs/plans/2026-09-21-idempotency-consumer-gaps.md` (the three passages, this epic's ticks)
- Modify: `README.md:67`
- Modify: `docs/PROJECT_RULES.md` (the `net/http/idempotency` rows only — carve-out)

**Verification:** `grep -rn -i 'releases the key\|RELEASES' README.md docs/PROJECT_RULES.md docs/plans/2026-09-21-idempotency-consumer-gaps.md` returns only sentences that name the reversal or `ClientErrorPolicyRelease`.

**Done when:** a reader of any of the three documents learns the shipped behaviour on first contact.


### Epic 1.4: Review round 3 residue (added by the orchestrator, 2026-09-21)

**Goal:** the connection-retirement guard that round 3 added says when it fires and is tested on both halves — a replay with a provider, and a refusal with none — and the behaviour it adds is written where a consumer reads.
**Scope:** `commons/net/http/idempotency/{idempotency.go,doc.go,fingerprint_provider_test.go,replay_headers_test.go}`, `README.md:67`, this plan.
**Dependencies:** Epic 1.3 (landed).
**Done when:** `go test -tags=unit -race -count=1 ./commons/net/http/idempotency/...` green; the task ticked; the Phase Overview row for Phase 1 reads `Complete`.

**Status:** Done

**Stop rule (orchestrator, 2026-09-21):** this is the last harness round for this branch. Findings after it that change no behaviour — comments, records, messages — are fixed by the orchestrator directly or accepted with a written reason, and the pull request opens.

#### Task 1.4.1: The retirement guard's contract is stated, tested on the refusal half, and documented for consumers

- [x] Done

**Context:** commit `03f8fb1` added `retireUnreadRequestStream` (`idempotency.go:~814-830`): when the middleware answers instead of the handler while the request body is still a stream, it sets `Connection: close`, because nothing downstream will ever drain that body and the next request on the keep-alive connection would read it as its own. Its doc comment says "nothing fires without a provider". That is false and the behaviour is right: `handle()` registers the deferred guard (`:~1103`) BEFORE the five refusal paths that return without reaching `resolveFingerprint` (`:~1161`, the only site that calls `c.Body()` and thereby drains the stream) — over-length key, missing required key, and their siblings — so on a `StreamRequestBody` route with no provider a refusal also retires the connection, correctly, and no test covers that half (F1/F2/F4, measured by the reviewers over a real keep-alive connection with a 70 KiB body and an over-length key). Two small items: `TestReplay_LiveCookieFromOtherMiddleware_Survives`'s csrf assertion message (`replay_headers_test.go:~260`) claims to guard captured-cookie-name de-duplication, but under the delta capture the csrf cookie is never captured (F3); and `Connection: close` on a replay or refusal of a streamed request is consumer-visible behaviour stated in no consumer-facing document (F5).

**Implementation vision:** (a) rewrite the guard's doc comment to the true rule: it fires whenever the middleware answers without running the handler and the body is still a stream — a replay under a fingerprint provider, or any refusal that returns before the fingerprint read — and stays silent when the handler ran or when `c.Body()` already drained the stream. (b) extend `TestFingerprintProvider_StreamedDuplicate_LeavesTheConnectionUsable`'s raw HTTP/1.1 harness with the refusal half: a `StreamRequestBody: true` app, NO provider, `WithMaxKeyLength(8)`, a 70 KiB POST under an over-length key on a keep-alive connection, then a second well-formed request on the same connection — assert the refusal carries `Connection: close`, the client reconnects and the second request is answered, and (RED) with the guard's deferred call removed the second request is reset or misparsed. Name the test for what it pins (`TestRefusal_StreamedBodyUnread_RetiresTheConnection`). (c) csrf message: say what it asserts — the live, per-request csrf value survives because the cookie is never captured — and nothing about de-duplication; the DelCookie loop is pinned by `TestReplay_LiveCookieCollidesWithCapturedName_ReplacedNotDuplicated`. (d) document the behaviour once in `doc.go` (a short paragraph under the streamed/multipart discussion: what happens, why, what a pooled client sees — one reconnect, no lost request) and one clause in `WithFingerprintProvider`'s godoc pointing at it; the README bullet gains the words "answers to a streamed request that the middleware refuses or replays close the connection". Then flip the Phase Overview row to `Complete` and tick this task.

**Files:**
- Modify: `commons/net/http/idempotency/idempotency.go` (the doc comment only)
- Modify: `commons/net/http/idempotency/fingerprint_provider_test.go`
- Modify: `commons/net/http/idempotency/replay_headers_test.go:~260`
- Modify: `commons/net/http/idempotency/doc.go`
- Modify: `README.md:67`
- Modify: `docs/plans/2026-09-21-idempotency-consumer-gaps.md`

**Verification:** `go test -tags=unit -race -count=1 ./commons/net/http/idempotency/...` green; the RED excerpt for (b) recorded below this task.

**Done when:** the guard's comment, its two tests and the consumer docs agree on one rule.

**RED for (b), measured 2026-09-21** with `defer m.retireUnreadRequestStream(c)` removed from `handle()`, `go test -tags=unit -race -count=1 -run TestRefusal_StreamedBodyUnread_RetiresTheConnection ./commons/net/http/idempotency/...`:

```
--- FAIL: TestRefusal_StreamedBodyUnread_RetiresTheConnection (0.00s)
    fingerprint_provider_test.go:744: Should be true
        the refusal answered without running the handler, so the upload was never read;
        keeping the connection leaves the next request parsed from the middle of it
    fingerprint_provider_test.go:748: Received unexpected error:
        write tcp 127.0.0.1:39456->127.0.0.1:36231: write: connection reset by peer
        the connection died before this request was even sent
```

The second assertion is the one that matters: with the guard gone the refusal keeps the connection, and the request that follows it on that connection is reset. The guard restored, the whole package is green and the client dials exactly twice — one connection per answer the middleware gave itself, no request lost.

#### Round 4 residue (orchestrator, 2026-09-22)

Three items, none of which changes behaviour. The guard `2eade46` narrowed has three clauses and only one of them was held by a test: measured with `go test -overlay` deleting each clause in turn, the package stayed green on two. A wrong deletion poisons live connections, so both are now fenced by a failing test.

1. **The chunked clause (`contentLength >= 0`) was unheld.** `TestRefusal_StreamedBodyUnread_RetiresTheConnection` now runs three cases: the original 70 KiB declared-length refusal, and a chunked refusal at 10 bytes and at 200 KB. A chunked upload declares no length, fasthttp reports `-1` and `readBodyWithStreaming` refuses chunked outright, so NOTHING is pre-read and ten bytes are as unread as 200 KB. Read as a number rather than as "no declared length", that `-1` sorts below every threshold and the guard keeps exactly the connections it exists to retire. The keep-alive probe gained `postChunked`. Commit `557899a`.
2. **The body-limit clause (`contentLength <= c.App().Config().BodyLimit`) was unheld.** `TestRefusal_StreamedBodyBuffered_KeepsTheConnection` is now parameterised over the app's body limit: the original walk under the default 4 MiB limit (where the 8 KiB ceiling binds, 0/200/8192 kept and 8193 retired), and a second app at `BodyLimit: 4096` where the limit binds instead — 4096 kept, 4097 retired, redial, next request answered by the app. fasthttp copies `min(bodyLimit, Content-Length, 8 KiB)`, so on a 4 KiB route a 4097-byte body is a stream with exactly one byte still in the socket. Commit `557899a`.
3. **`TestReplay_LiveCookieFromOtherMiddleware_Survives`'s doc comment named the wrong half of the branch.** Rewritten to what it pins: a cookie minted above the middleware reaches the client carrying THIS request's value. The capture here DOES hold a `Set-Cookie` (the handler's `session`), so the replay clears under that name and the live cookies survive only because the clearing is scoped to the cookie names being re-applied. It does NOT pin replacement-over-duplication — no live cookie here collides with a captured name — which is `TestReplay_LiveCookieCollidesWithCapturedName_ReplacedNotDuplicated`'s job. The comment now says which half each fences. Commit `bc432b9`.

**RED for (1), measured 2026-09-22** with `contentLength >= 0 &&` deleted from the guard, `go test -overlay=… -tags=unit -race -count=1 -run TestRefusal_StreamedBodyUnread_RetiresTheConnection ./commons/net/http/idempotency/` (trimmed):

```
--- FAIL: TestRefusal_StreamedBodyUnread_RetiresTheConnection/chunked_10_bytes (0.01s)
    fingerprint_provider_test.go:812: Should be true
        no byte of a chunked body is pre-read, so this 10-byte refusal left all of it
        in the connection: the next request on it is parsed from the chunk framing
    fingerprint_provider_test.go:817: Not equal:
        expected: 201
        actual  : 400
        the client reconnects and its next request is answered by the app: the refusal
        costs a connection, never a request
--- FAIL: TestRefusal_StreamedBodyUnread_RetiresTheConnection/chunked_204800_bytes (0.01s)
    fingerprint_provider_test.go:812: Should be true
        no byte of a chunked body is pre-read, so this 204800-byte refusal left all of it
        in the connection: the next request on it is parsed from the chunk framing
    fingerprint_provider_test.go:816: Received unexpected error:
        write tcp 127.0.0.1:41156->127.0.0.1:38517: write: connection reset by peer
        the connection died before this request was even sent
```

At 10 bytes the leftover chunk framing is parsed as the next request line and answered `400`; at 200 KB the server resets the socket before the follow-up is even written. The declared-length subtest stays green throughout, which is the point: size held the clause, framing did not.

**RED for (2), measured 2026-09-22** with `&& contentLength <= c.App().Config().BodyLimit` deleted from the guard, `-run TestRefusal_StreamedBodyBuffered_KeepsTheConnection` (trimmed):

```
--- FAIL: TestRefusal_StreamedBodyBuffered_KeepsTheConnection/bounded_by_a_smaller_body_limit (0.01s)
    fingerprint_provider_test.go:935: Should be true
        one byte past the route's limit, fasthttp stopped copying at 4096 and that byte
        is still in the connection: the next request on it is parsed starting from it
    fingerprint_provider_test.go:940: Not equal:
        expected: 201
        actual  : 501
        the client reconnects and its next request is answered by the app: a retirement
        costs a connection, never a request
```

`501 Not Implemented`: the leftover `x` prefixes the next request line, fasthttp reads the method as `xPOST` and rejects it. The `bounded_by_the_8k_pre_read` subtest stays green under the same deletion, which is why the clause needed its own app rather than another size in the existing walk.

**Correction to item (3)'s premise.** The round-4 finding held that the csrf cookie is never captured, therefore `clearCapturedHeader`'s Set-Cookie branch is not reached in this test. The second half does not follow and is false: the handler's own `session` cookie IS captured, so the branch runs. Measured with the `DelCookie` loop replaced by `c.Response().Header.Del(name)` — fasthttp's whole-jar wipe — `TestReplay_LiveCookieFromOtherMiddleware_Survives` fails with `counts["csrf"]` and `counts["locale"]` both `0` and `values["csrf"]` empty, while `TestReplay_LiveCookieCollidesWithCapturedName_ReplacedNotDuplicated` stays green. Drop the clearing entirely and the pair inverts. The two tests fence the branch from opposite sides, and the rewritten comment says so rather than repeating the false premise.


## Bugs found

| Id | File | Symptom | Fix commit |
|---|---|---|---|
| B1 | `commons/net/http/idempotency/idempotency.go` | An over-cap response completed the key as carrying no replayable receipt whatever its status, so a 4xx cached under the default client-error policy answered a resend `409 IDEMPOTENCY_REPLAY_UNAVAILABLE`: "already completed successfully". The mutation was REJECTED and committed nothing, and the rejection document was unreachable for the whole retention window. A non-2xx now completes with the unrecorded outcome, whose refusal asserts nothing. **Superseded by Task 1.2.2:** the unrecorded outcome still asserted something false ("reconcile the original request") about a rejection that committed nothing, so an over-cap 4xx now RELEASES the key instead of completing it at all. **Reversed 2026-09-21 by `d058590` (row B5):** size decides nothing — an over-cap response of any status completes with `outcomeNotReplayable` and its resend is refused 409 `IDEMPOTENCY_REPLAY_UNAVAILABLE`; only `ClientErrorPolicyRelease` releases a 4xx, and it does so before the response is captured. | `f22a637`, `ae90297`, `d058590` |
| B2 | `commons/net/http/idempotency/idempotency.go` | A `WithResponseCodec` producing zero bytes returned the same error as an over-cap body, so a codec malfunction silently completed every response on the route as non-replayable while the WARN log and the client document blamed `WithMaxBodyCache`, which cannot fix it. An empty encoding now has its own error and keeps the loud post-handler failure. | `f22a637` |
| B3 | `commons/net/http/idempotency/idempotency.go` | Task 1.1.2's `Header.Del(name)` hits fasthttp's special case for `Set-Cookie` and empties the WHOLE cookie jar, so a replay discarded a cookie another middleware had minted on that request — a rotated session, a fresh CSRF token — and handed back the captured one, whose next mutation the CSRF check refuses. Captured cookies are now cleared one cookie name at a time through `DelCookie`. | `f22a637` |
| B5 | `commons/net/http/idempotency/idempotency.go` | An over-cap 4xx released the key whatever `WithClientErrorPolicy` said, and since `ClientErrorPolicyRelease` releases before the capture runs, that arm only ever fired against the default `ClientErrorPolicyCache`. A validation route one byte over the bound re-executed its rejection path on every resend — duplicating whatever that path writes — while the same route with a shorter report did not. Size no longer decides: an over-cap response completes the same way whatever its status, and the 409 refusal stopped claiming a success for a request that was rejected. | `d058590` |
| B6 | `commons/net/http/idempotency/idempotency.go` | `captureHeaderDelta` walked only the post-handler header map, so a header the handler DELETED was never captured and the replay left the live value in place. A receipt route that strips helmet's `X-Frame-Options` so a partner can iframe it answered the duplicate WITH the header: the partner's page broke on the retry and not on the first attempt. A removal is now captured as an empty value list. Cookie removals are deliberately out of scope (a cookie is identified by name and a removal has no value to re-apply); doc.go states it. | `a14b4cc` |
| B7 | `commons/net/http/idempotency/replay_headers_test.go` | The cookie-aware clearing on replay was untested: every existing cookie assertion minted names that never collide with the capture, so deleting the `DelCookie` loop left the whole package green. Measured with it removed, a session rotator mounted above a route whose handler also mints `session` produced two `Set-Cookie: session=…` on the replay (`[live-2 captured]`) and the client keeps whichever its stack picks — one of them belonging to the other request. Closed by `TestReplay_LiveCookieCollidesWithCapturedName_ReplacedNotDuplicated`. | `408486a` |
| B4 | `commons/net/http/idempotency/fingerprint_provider_test.go` | Task 1.1.1 shipped with no test proving the provider's bytes reach the digest: dropping the identity from `resolveFingerprint` left the whole package green. A client uploading `june.csv` then `july.csv` under one key would have received the first upload's receipt without the handler running. Closed by `TestFingerprintProvider_DifferentIdentityIsReuse`. | `9d29271` |
| B8 | `commons/net/http/idempotency/idempotency.go` | With `WithFingerprintProvider` set nothing in the middleware calls `c.Body()`, so a request answered WITHOUT running the handler — a replay, or any refusal — left a streamed upload unread in the connection. fasthttp recycles the stream struct without draining the reader, so the next request on a keep-alive connection is parsed from the middle of this one's body and the connection is reset. Measured over one real keep-alive connection, 70 KiB body, one key: the third POST died with `connection reset by peer` with a provider set, while the same three all answered without one. A client retrying a large upload under the published same-key contract collected its replay and lost the pooled connection under it, and whatever the pool multiplexed onto it next died as a network error. Such a request now answers `Connection: close` — what net/http sends for an unread body, and what a client's pool understands; draining instead would mean reading up to the route's body limit to answer a 409. | `03f8fb1` |
| B9 | `commons/net/http/idempotency/oversize_response_test.go` | Task 1.3.1(d)'s two WARN fields (`idempotency_key_digest`, `tenant_id`) were held by no test: deleting both pairs left the whole package green. That branch is the one that makes a key unreplayable for its whole retention, so those fields are the only way to tie a stream of 409 refusals back to one record. Closed by `TestOversizeResponse_BothWarningsNameTheRecord`, proven RED against the deletion on both lines. | `33ed966` |
| B10 | the branch's commit range | The over-cap HTTP contract changed under consumers already wired on `^7` — the original request and its resend both answer differently, and `WithPostHandlerUnavailableHandler` no longer owns that path — but no commit in the range carried `!` or a `BREAKING CHANGE:` footer, so the release would have shipped it with an empty breaking section. The footer now rides a doc.go note telling an upgrader the same thing. `.releaserc.yml` anchors breaking to minor, so the version is unaffected. | `f20e20b` || B11 | `commons/net/http/idempotency/idempotency.go` | B8's retirement guard fired for every request fasthttp reported as a stream, including bodies it had already drained in full: `readBodyWithStreaming` lifts `min(bodyLimit, Content-Length, 8 KiB)` out of the connection BEFORE it creates the stream, and creates one whatever it copied, so a body at or under that bound is a stream with an empty reader behind it. Since Fiber's `StreamRequestBody` is an APP-WIDE setting, a service that turned it on for its one large-upload route retired the connection on every route: a client retrying a 200-byte JSON mutation paid a fresh TCP (and TLS) handshake per duplicate that `origin/develop` never charged, and a retry storm became connection churn at the pool and at any L7 proxy in front — cost, not corruption, but the guard's stated rule ('nobody read the upload') was not the condition it tested. The guard now also requires the connection to hold a remainder: a declared length past the pre-read, or a chunked body, of which nothing is pre-read. `TestRefusal_StreamedBodyBuffered_KeepsTheConnection` walks the boundary (0, 200, 8192, 8193 bytes) over one real keep-alive socket, so the unexported fasthttp constant is pinned by the requests that follow a kept connection rather than by a comment; measured RED against the unnarrowed guard at 5 dials for 4 refusals where 2 are correct. | `2eade46` |

## Bugs found outside this package

| File | Symptom | Evidence |
|---|---|---|
| `README.md` (the `commons/net/http/idempotency` bullet) | The repo's canonical contract for this middleware contradicts the shipped behaviour in four places after this lane: it still says an exact response that "cannot be captured" fails closed with 503 `IDEMPOTENCY_UNAVAILABLE` and that response-capture failure "additionally fences the key", both now false for an over-cap body; its exhaustive list of customizable rejection bodies omits `WithReplayUnavailableHandler`; its duplicate-outcome list omits `IDEMPOTENCY_REPLAY_UNAVAILABLE`; and its fingerprint paragraph names only `WithKeyProvider` and `WithFingerprintScopeProvider`, with `WithFingerprintProvider` absent. An integrator wiring from the README wires `WithPostHandlerUnavailableHandler` expecting it to own the over-cap resend and ships the library's raw 409 envelope instead; a consumer with a multipart or streamed-upload route never learns the option that unblocks it. | `README.md:67`, read against `commons/net/http/idempotency/doc.go` and `idempotency.go` on this branch. The file is outside this lane's carve-out (`commons/net/http/idempotency/**` and this plan), so it is recorded rather than edited — a lane-scoping gap, not an implementer slip. **FIXED HERE by Task 1.2.3**, after the orchestrator widened the carve-out to include this bullet on 2026-09-21. |
| `docs/PROJECT_RULES.md` (line 577, the `commons/net/http/idempotency` row of the canonical rules table) | The repo's canonical rules table contradicts the shipped middleware in two clauses and omits both of this lane's new options. It still reads "Exact replay is mandatory after a handler succeeds: capture, codec, completion, stale-owner, missing-response, and decode failures return 503 IDEMPOTENCY_UNAVAILABLE; no generic success fallback exists" — after this branch a SIZE-caused capture failure returns neither a 503 nor a replay: the response is delivered unchanged and the record completes with no receipt, and the resend is 409 `IDEMPOTENCY_REPLAY_UNAVAILABLE`. The same row's option list names only `WithResponseCodec`, `WithTTLProvider`, `WithFingerprintScopeProvider` and `WithClientErrorPolicy`: `WithFingerprintProvider` and `WithReplayUnavailableHandler` are absent, as is the fourth refusal code. An integrator wiring from this table wires `WithPostHandlerUnavailableHandler` expecting it to own the over-cap resend and ships the library's raw 409 envelope instead, and a team with a multipart or streamed-upload route never learns the option that unblocks it. Same defect class as the README row above, one file further out. | `docs/PROJECT_RULES.md:577`, read against `commons/net/http/idempotency/doc.go` and `idempotency.go` on this branch. Was outside this lane's carve-out (`commons/net/http/idempotency/**`, this plan, and the one README bullet), so it was recorded rather than edited. **FIXED HERE by Task 1.3.2**, after the orchestrator widened the carve-out to the `net/http/idempotency` rows of that file on 2026-09-21: the row now names the size branch as a non-fault with its 409, all four exported refusal codes, and the complete option list including `WithFingerprintProvider`, `WithReplayUnavailableHandler` and `WithMaxBodyCache`. No other row of the file was touched. |
