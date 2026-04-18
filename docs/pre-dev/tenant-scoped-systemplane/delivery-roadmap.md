# Delivery Roadmap: Tenant-Scoped Systemplane

## Inputs

| Input | Value |
|-------|-------|
| Feature | tenant-scoped-systemplane |
| Start date | 2026-04-20 (first effective workday, Monday) |
| Team size | 1 (solo) |
| Cadence | Continuous (no period boundaries) |
| Multiplier | 1.2x (agent-first, light-touch human validation) |
| AI-hours | 74-96h (point ~85h) |
| Capacity factor | 0.90 (10% for context/tool overhead) |

## Headline Numbers

- **Point estimate:** ~14 calendar days (business days, weekends skipped)
- **Range:** 12-16 days
- **With 15% contingency:** 14-18 days
- **Target completion:** 2026-05-07 (point, no contingency)
- **Target completion with contingency:** 2026-05-12 (15% buffer absorbed)

### Formula and show-work

```
effective_hours = ai_hours × multiplier ÷ 0.90
calendar_days   = effective_hours ÷ 8 ÷ team_size

Point:  85 × 1.2 ÷ 0.90 = 113.3h  → 113.3 ÷ 8 ÷ 1 = 14.17 days
Low:    74 × 1.2 ÷ 0.90 = 98.67h  → 98.67 ÷ 8 ÷ 1 = 12.33 days
High:   96 × 1.2 ÷ 0.90 = 128.0h  → 128.0 ÷ 8 ÷ 1 = 16.0 days

15% contingency on point = 14.17 × 0.15 = 2.13 days (~2 business days)
```

### Per-task effective hour derivation

Each task's point AI-hours are transformed to effective calendar-hours using the same multiplier/capacity math (×1.333 factor):

| Task | AI-hours (point) | Effective hours | Days @ 8h/day |
|------|-----------------:|----------------:|--------------:|
| 1 — Store interface delta + TestStore mirror | 3.5 | 4.67 | 0.58 |
| 2 — Client-side tenant state scaffolding | 5.0 | 6.67 | 0.83 |
| 3 — Postgres tenant storage + schema + trigger | 12.0 | 16.00 | 2.00 |
| 4 — MongoDB tenant storage + index + delete-event | 12.0 | 16.00 | 2.00 |
| 5 — Client dataflow + changefeed + OnTenantChange | 12.0 | 16.00 | 2.00 |
| 6 — Admin HTTP tenant routes + WithTenantAuthorizer | 6.0 | 8.00 | 1.00 |
| 7 — api_compat pin + Client-layer unit suite | 9.0 | 12.00 | 1.50 |
| 8 — Integration + race + benchmark tests | 13.0 | 17.33 | 2.17 |
| 9 — Migration doc + CLAUDE.md + README | 4.5 | 6.00 | 0.75 |
| **Total** | **77.0** | **102.67** | **12.83** |

*Note: the task-sum point (77h) lands below the prompt's locked 85h point. The 85h figure absorbs integration overhead, reviewer cycles, and unforeseen cross-backend subtleties that don't attribute cleanly to a single task. The roadmap honors the 85h point as the headline number and uses the 77h task-sum for sequencing.*

## Critical Path

Solo mode = strictly sequential. The DAG from `tasks.md` allows parallelism (Tasks 3 and 4 are independent after Task 1/2), but with one developer every task serializes. Critical path equals task order **1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9**.

| # | Task | Hours (eff) | Cumulative h | Cum. days | Calendar end |
|---|------|------------:|-------------:|----------:|--------------|
| 1 | Store interface delta + TestStore mirror | 4.67 | 4.67 | 0.58 | 2026-04-20 |
| 2 | Client-side tenant state + registry + LRU | 6.67 | 11.33 | 1.42 | 2026-04-21 |
| 3 | Postgres tenant storage + schema + trigger | 16.00 | 27.33 | 3.42 | 2026-04-23 |
| 4 | MongoDB tenant storage + index + delete-event | 16.00 | 43.33 | 5.42 | 2026-04-27 |
| 5 | Client dataflow + changefeed routing + OnTenantChange | 16.00 | 59.33 | 7.42 | 2026-04-29 |
| 6 | Admin HTTP tenant routes + WithTenantAuthorizer | 8.00 | 67.33 | 8.42 | 2026-04-30 |
| 7 | api_compat pin + Client-layer unit suite | 12.00 | 79.33 | 9.92 | 2026-05-04 |
| 8 | Integration + race + benchmark tests | 17.33 | 96.67 | 12.09 | 2026-05-06 |
| 9 | Migration doc + CLAUDE.md + README changelog | 6.00 | 102.67 | 12.83 | 2026-05-07 |

Cumulative matches point target ~14 calendar days (12.83 days rounded up for conservatism). All tasks land on/before 2026-05-07 point target. With contingency absorbed, still lands before 2026-05-12.

## Task Sequence (solo mode = strictly sequential)

| # | Task | AI-hours | Effective h | Start | End | Confidence | Risk |
|---|------|---------:|------------:|-------|-----|------------|------|
| 1 | Store interface delta + TestStore mirror + Entry/Event evolution | 3-4 (3.5) | 4.67 | 2026-04-20 Mon | 2026-04-20 Mon | **H** | — |
| 2 | Client-side tenant state, registry, and cache scaffolding | 4-6 (5.0) | 6.67 | 2026-04-20 Mon | 2026-04-21 Tue | **M** | LRU wrapper subtlety |
| 3 | Postgres tenant storage + schema evolution + trigger rewrite | 10-14 (12) | 16.00 | 2026-04-21 Tue | 2026-04-23 Thu | **M** | DDL on existing populated table; PK drop race |
| 4 | MongoDB tenant storage + index evolution + delete-event handling | 10-14 (12) | 16.00 | 2026-04-23 Thu | 2026-04-27 Mon | **L** | **Delete-event semantics + index rebuild** |
| 5 | Client read/write/delete + changefeed routing + debouncer widening + OnTenantChange | 10-14 (12) | 16.00 | 2026-04-27 Mon | 2026-04-29 Wed | **M** | Changefeed routing with cache-update ordering is genuinely new logic |
| 6 | Admin HTTP tenant routes + WithTenantAuthorizer + default-deny | 5-7 (6.0) | 8.00 | 2026-04-29 Wed | 2026-04-30 Thu | **H** | Authorizer fallback semantics only |
| 7 | api_compat pin + comprehensive Client-layer unit test suite | 8-10 (9.0) | 12.00 | 2026-05-01 Fri | 2026-05-04 Mon | **M** | 15+ test count + debouncer-timing tests |
| 8 | Integration + race + benchmark tests (Postgres + MongoDB) | 12-14 (13) | 17.33 | 2026-05-04 Mon | 2026-05-06 Wed | **L** | **testcontainers reliability, race detection surprises, sub-µs perf target** |
| 9 | Migration doc + CLAUDE.md systemplane-section update + README changelog | 4-5 (4.5) | 6.00 | 2026-05-07 Thu | 2026-05-07 Thu | **H** | — |

Weekend gaps handled: Apr 25-26 (between T4 work days), May 2-3 (between T6 and T7).

## Risk Milestones

Low-confidence tasks are the critical risk markers. Both land mid-to-late schedule — surface them early for proactive de-risking.

| Milestone | Date | Risk | Mitigation |
|-----------|------|------|------------|
| Task 4 completion (MongoDB backend) | 2026-04-27 Mon | MongoDB delete-event has no `fullDocument`; index rebuild on existing data; polling-mode tenant event parity | Run `testcontainers-go` replica-set integration test (mongodb.WithReplicaSet("rs0")) before moving to Task 5. Confirm re-read fallback handles delete correctness per TRD §3.2. Draft a manual smoke of the old-index-drop sequence on a dirty collection. |
| Task 8 completion (integration + race + bench) | 2026-05-06 Wed | testcontainers orchestration; MongoDB replica-set bootstrapping is brittle; 100-goroutine race test may surface new races; sub-microsecond `GetForTenant` hit target may need profile-guided tuning | **Allocate all contingency buffer here first.** Budget up to 2 additional business days on this single task if benchmarks miss PRD AC15. Consider defensive split of Task 8 into 8a (contract+integration) + 8b (race+bench) if day-14 pressure mounts. |

## Contingency Allocation

- **Buffer size:** 15% of point = 2.13 calendar days (~2 business days)
- **Placement:** Immediately after Task 8 (highest-risk task). If T8 overruns, the buffer absorbs it without pushing T9 past 2026-05-12.
- **If contingency unused:** feature ships on 2026-05-07, 2-3 business days early.
- **Trigger for escalation:** if Task 4 or Task 8 consumes >1.5× its point estimate, notify the user and re-plan scope (e.g., drop MongoDB polling-mode tenant parity from Task 4 or defer benchmarks from Task 8 to a follow-up).

## Parallelization Opportunities (Unused in Solo Mode)

Documented for reference if team grows to 2+ developers mid-project. Solo critical path uses none of this — all tasks serialize.

- **Task 3 (Postgres) ∥ Task 4 (MongoDB)** — both depend only on Task 1/2. Disjoint file sets; shared only through the Store interface. Savings: ~10h off critical path if fully parallel.
- **Task 2 ∥ Task 3 ∥ Task 4** — Task 2 touches Client scaffolding; Tasks 3/4 touch internal backends. All three can fork after Task 1. Savings: ~4h.
- **Task 7 (unit tests) can start after Task 2** — API surface exists for TDD-style pre-writing once `RegisterTenantScoped` signature is fixed. Risky for estimation confidence; conservative default is strictly sequential.
- **Task 9 (docs) can start after Task 5** — behavior is locked at that point. Docs then refine in parallel with Tasks 6-8.
- **Task 6 (admin) can overlap the last hour of Task 5** — handlers only need `SetForTenant`/`DeleteForTenant`/`ListTenantsForKey` wired.

Maximum parallelization with 2 devs shortens critical path to Task 1 → (max of 2, 3, 4) → 5 → 6 → 7 → 8 → 9 ≈ ~52 effective hours ≈ 8-9 business days.

## Delivery Milestones

Five checkpoint milestones slice the 14-day roadmap into reviewable bands:

- **M1: Foundation (Tasks 1-2)** — 2026-04-20 Mon to 2026-04-21 Tue
  - Deliverables: `Store` interface grows 5 methods; `TestStore` mirror; `Entry{TenantID}`/`Event{TenantID}` widened; Client struct carries tenant state; `RegisterTenantScoped` + `WithLazyTenantLoad` in-memory only.
  - Exit gate: `go build ./commons/systemplane/...` succeeds; smoke test verifies `testStoreAdapter` forwards `TenantID`; AC2 holds in-memory.

- **M2: Postgres Path (Task 3)** — 2026-04-21 Tue to 2026-04-23 Thu
  - Deliverables: schema evolution (column + backfill + compound index); NOTIFY trigger with `tenant_id` + DELETE support; 5 new Store methods end-to-end on Postgres.
  - Exit gate: Postgres testcontainer round-trip of `SetTenantValue` → `GetTenantValue` → `ListTenantsForKey`. AC3/AC9/AC10 hold on Postgres.

- **M3: MongoDB Parity (Task 4)** — 2026-04-23 Thu to 2026-04-27 Mon
  - Deliverables: backfill `UpdateMany`; old index drop; compound index create; `$match` extended with `"delete"`; delete-event fallback via `documentKey._id` + re-read; polling-mode tenant parity.
  - Exit gate: MongoDB replica-set testcontainer green on same round-trip battery. AC13 (backend parity) foundation laid.

- **M4: Subscriber Semantics (Tasks 5-6)** — 2026-04-27 Mon to 2026-04-30 Thu
  - Deliverables: `OnTenantChange` dispatch; changefeed routing by `tenant_id`; debouncer key widening (Namespace + Key + TenantID); admin HTTP tenant routes; `WithTenantAuthorizer` + default-deny escalation.
  - Exit gate: AC7 (OnTenantChange fire), AC8 (OnChange non-fire on tenant writes — the single most critical regression pin), AC12 (debouncer correctness), AC14 (admin routes) all pass.

- **M5: Tests + Docs (Tasks 7-9)** — 2026-05-01 Fri to 2026-05-07 Thu
  - Deliverables: `api_compat_test.go` signature pin; ~15 Client-layer unit tests; extended contract suite in `systemplanetest`; Postgres + MongoDB integration tests; race test (100 goroutines × distinct tenants); benchmarks meeting AC15 (<1µs hit, <2µs miss); `MIGRATION_TENANT_SCOPED.md`; CLAUDE.md section update; README changelog.
  - Exit gate: `make ci` green; `make test-integration` green on both backends; `go test -bench=BenchmarkGetForTenant` meets perf budget; docs reviewed.

## Flags and deviations from target

- **No task pushes past 2026-05-12.** Even at upper-bound estimates (96 AI-hours, 16 calendar days), completion lands on 2026-05-12 Tue — the contingency target exactly. No explicit re-plan needed.
- **Task 8 at upper bound (14 AI-hours) is the one to watch.** 14 × 1.333 = 18.67h = 2.33 business days. If Task 8 overruns to its upper bound, cumulative hits 12.25 business days ending mid-day Wed 2026-05-06, still safe.
- **If Task 4 AND Task 8 both hit upper bounds simultaneously**, cumulative hours climb to 77 + 2 + 1 = 80h AI = 106.67 effective = 13.33 business days. Still under 14-day point, but leaves only 0.8 day buffer for Task 9. Trigger re-plan if both low-confidence tasks surface material unknowns.

## Gate Approvals

- [ ] Gate 4 approved by: Fred Amaral
- [ ] Date: 2026-04-18
