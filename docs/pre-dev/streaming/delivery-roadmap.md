# Delivery Roadmap — `commons/streaming/`

## 1. Metadata

| Field | Value |
|---|---|
| Title | Delivery roadmap — streaming wrapper harness |
| Feature | `commons/streaming/` |
| Gate | 4 (Delivery planning — Small Track final gate) |
| Version | v1 |
| Created | 2026-04-18 |
| Start date | 2026-04-20 (Mon) |
| Baseline end date | 2026-05-01 (Fri) |
| Declared delivery target (with contingency) | 2026-05-05 (Tue) |
| Total AI-agent-hours (T1–T10) | 60h |
| Effort hours with multiplier + capacity | 80h (10 working days) |
| Confidence roll-up | 4 High · 5 Medium · 2 Low |
| Upstream | [`tasks.md`](./tasks.md) · [`trd.md`](./trd.md) · [`prd.md`](./prd.md) |

---

## 2. Inputs summary

| Input | Value | Source |
|---|---|---|
| Start date | 2026-04-20 (Mon) | Default: next business day after 2026-04-18 (Sat) |
| Team size | 1 developer (solo) | Locked |
| Cadence | Continuous delivery — no sprint / cycle boundaries | Locked |
| Human validation multiplier | 1.2x (light review, self-contained library) | Locked |
| Available capacity | 0.90 (Ring AI Agent standard: 10% overhead) | Locked |
| Workday length | 8 AI-agent-hours | Locked |
| Workweek | Mon–Fri (weekend work not planned) | Locked |
| Stretch T11 (Grafana dashboard, 3h) | Optional sidebar — excluded from baseline critical path | Locked |

Period-boundary note: because cadence is continuous, there are no sprint boundaries to enforce. Tasks merge on their own pace and the roadmap tracks accumulated hours against working-day capacity.

---

## 3. Formula application

Formula (verbatim): `calendar_days = (ai_estimate × multiplier / 0.90) / 8 / team_size`

Baseline (T1–T10, excluding T11 stretch):

| Step | Calculation | Result |
|---|---|---|
| Total AI-hours from `tasks.md` | T1..T10 | **60h** |
| Apply multiplier (1.2x) | 60 × 1.2 | 72h |
| Adjust for 90% capacity | 72 / 0.90 | 80h |
| Convert to 8h working days | 80 / 8 | 10 working days |
| Divide by team size (1) | 10 / 1 | **10 working days** |
| Map onto Mon–Fri from 2026-04-20 | Wk1 Mon 04-20 → Fri 04-24 (5d) · Wk2 Mon 04-27 → Fri 05-01 (5d) | Last working day: **Fri 2026-05-01** |
| Calendar span | 2026-04-20 → 2026-05-01 | 12 calendar days (includes 1 weekend) |

Critical path (informational for solo developer):

| Step | Calculation | Result |
|---|---|---|
| Critical path AI-hours | T1→T2→T3→T4→T5→T6→T7→T9→T10 (T10 parallel w/ T9) | 46h |
| Apply multiplier | 46 × 1.2 | 55.2h |
| Adjust for 90% capacity | 55.2 / 0.90 | 61.33h |
| Convert to working days | 61.33 / 8 | **7.67 working days** |

Solo execution still burns the full 60h baseline; the 7.67-day critical path is an upper bound on parallel compression — it becomes a lever only if the team scales up. For Gate 4 the scheduling driver is the full 10 working days.

---

## 4. Gantt-style timeline

Task IDs are placed in the cell that represents the dominant work for that day. A cell marked `PARALLEL?` notes where a second developer could peel off work if the team ever scales.

### Week 1 — 2026-04-20 to 2026-04-24

| Day | Mon 04-20 | Tue 04-21 | Wed 04-22 | Thu 04-23 | Fri 04-24 |
|---|---|---|---|---|---|
| AM | T1 (start) | T1 (finish) | T2 (start) | T2 (finish) | T8 (2h) |
| PM | T1 cont. | T1 cont. | T2 cont. | T2 cont. | T3 (start) |
| Parallel lane | — | — | — | — | PARALLEL? T8 ↔ T3 start (would fit 2-dev team) |

### Week 2 — 2026-04-27 to 2026-05-01

| Day | Mon 04-27 | Tue 04-28 | Wed 04-29 | Thu 04-30 | Fri 05-01 |
|---|---|---|---|---|---|
| AM | T3 (finish) | T4 (finish) | T6 (finish) | T7 (finish) | T9 (cont.) |
| PM | T4 (start) | T5 · T6 (start) | T7 (start) | T9 (start) | T10 |
| Parallel lane | — | PARALLEL? T5 ↔ T6 after T2 (2-dev team) | — | PARALLEL? T10 ↔ T9 | T10 already parallel |

### Week 3 — 2026-05-04 onwards (contingency / spillover)

| Day | Mon 05-04 | Tue 05-05 | Wed 05-06 | Thu 05-07 | Fri 05-08 |
|---|---|---|---|---|---|
| Use | Contingency for T9 (1.5x case) | **Declared delivery target** · T11 stretch (optional) | T10 spillover if T9 hit 1.75x | Absolute worst case T9 double | Buffer / review cycle |

---

## 5. Milestones

| ID | Name | Target date | Scope |
|---|---|---|---|
| M1 | Core Emitter landable | 2026-04-24 (Fri, end Week 1) | T1, T2, T8 merged. Services can unit-test against `MockEmitter`; `NoopEmitter` available for feature-flag-off paths. |
| M2 | Broker integration stable | 2026-04-29 (Wed, mid Week 2) | T3, T4, T5 merged. Circuit breaker + outbox fallback + per-topic DLQ all functional. |
| M3 | Observability ready | 2026-04-30 (Thu, late Week 2) | T6 + T7 merged. SRE sees spans, metrics, logs in Grafana; `commons.App` bootstrap works; `Healthy` plugs into `HealthWithDependencies`. |
| M4 | v1 shippable | 2026-05-01 (Fri, end Week 2) | T9 integration suite green in CI; T10 docs + `.env.reference` + `CLAUDE.md` + `doc.go` example complete. |
| M5 | Optional polish (stretch) | 2026-05-05 (Tue, Week 3) | T11 Grafana dashboard JSON if time permits; otherwise slip to v1.1. |

---

## 6. Critical path

Sequential chain for solo execution: **T1 → T2 → T3 → T4 → T5 → T6 → T7 → T9 → T10**.

Notes:
- T5 and T6 both depend only on T2. For a solo developer they run sequentially; for a hypothetical 2-dev team they would parallelize after T4 lands.
- T8 (2h, `WithTLSConfig` / `WithSASL` plumbing) depends only on T2. It can slot anywhere post-T2 — parked on Fri 04-24 AM to keep the critical path clean.
- T10 (docs) depends on the final public surface (T1–T8) but parallels T9. Solo developer pulls T10 into Fri 05-01 alongside T9 finalization.
- Critical-path AI-hours = 46h → 7.67 working days when fully parallelized; not a compression lever for solo.

---

## 7. Spill-over analysis

T9 (integration tests, Low confidence, 12h baseline) is the principal spill-over risk. Per `tasks.md` §7, worst case adds +9h inflation (Redpanda container cold start, testcontainers API quirks, CloudEvents SDK strict-mode rejection, Toxiproxy wiring, flaky FIFO under constrained CI CPU).

| Scenario | T9 effort | Impact on schedule | Final ship date |
|---|---|---|---|
| T9 on estimate | 12h | T9 Thu PM → Fri AM; T10 Fri PM | **Fri 2026-05-01** |
| T9 at 1.5x | 18h | T9 spills into Mon Week 3; T10 Mon PM | Mon 2026-05-04 |
| T9 at 1.75x | 21h | T9 into Tue Week 3; T10 Wed | Wed 2026-05-06 |
| T9 at 2x (or split T9a/T9b per `tasks.md`) | 24h | T9 Tue Week 3; T10 Wed/Thu | Thu 2026-05-07 |

If T9 breaches the 16h-per-task cap, split as planned into T9a (round-trip + FIFO + CloudEvents contract) and T9b (DLQ + outbox fallback). Neither T10 nor later tasks are affected by this split because T10 parallels T9.

---

## 8. Parallel streams analysis

Team size = 1, so parallel streams are informational only. Recorded for the case where capacity scales up mid-flight:

| Stream | Tasks | Dependency | Notes |
|---|---|---|---|
| A (critical path, solo) | T1 → T2 → T3 → T4 → T5 → T6 → T7 → T9 → T10 | Fully sequential | Drives the 10-working-day total. |
| B (only if team scales to 2 devs) | T5, T6, T8 off the critical path after T2 | Each depends only on T2 | Would cut wall-clock by ~15–18h (roughly 2 working days). |
| C (stretch) | T11 dashboard after T6 merges | Depends on final metric names | 3h; can run any time after M3. |

This is a future-scaling hint, not a Week 1 action. Gate 4 baseline remains 10 working days.

---

## 9. Risks with time impact

| Risk | Trigger | Time impact | Mitigation |
|---|---|---|---|
| T9 testcontainers + Redpanda API quirks | First CI cold start; v0.41.0 API drift vs v0.33 docs | +6–9h | Pre-plan T9 split per `tasks.md` §7 (T9a round-trip/CloudEvents; T9b DLQ/outbox). |
| franz-go `ProducerLinger` default (10ms) latency regression | Week 2 benchmarking | +1–2h | Pin `ProducerLinger(5 * time.Millisecond)` explicitly; assert integration p50 < 20ms. |
| CloudEvents SDK v2.15.2 strict parsing rejects a header choice | T9 contract test fails | +2–4h | Single contract test round-trips Event through produce + parse in one shot. |
| Outbox-on-circuit-open race | Bursty traffic when breaker flips | +2h | Table-driven test in T4 forces the race condition. |
| Latent lib-commons bug surfaced during integration (wiring 5+ packages) | Mid Week 2 | +1–4h | Escalate via normal bug flow; does not block merge of unaffected layers. |

---

## 10. Contingency

| Scenario | Working days | Calendar end date |
|---|---|---|
| Baseline (no inflation) | 10 | Fri 2026-05-01 |
| 15% contingency (buffer 1.5 days rounded up) | 10 + 3 = 13 | Tue 2026-05-05 |
| Worst case (T9 at 2x) | ~14 | Thu 2026-05-07 |

**Declared delivery target: Tuesday 2026-05-05.**

That target absorbs one of the risks in §9 (or the 1.5x T9 scenario) without slipping into the following week. If the 2x T9 case materializes, the schedule still lands by 2026-05-07 — inside the original mid-May envelope.

---

## 11. Gate 5 (Review / Approval) handoff

This roadmap is the final Gate of the Small Track pre-dev workflow. After human approval the team can proceed to implementation:

- Use `/ring:worktree` to create an isolated workspace on `feat/adding-streaming`.
- Use `/ring:write-plan` to generate the implementation plan from `tasks.md` + this roadmap.
- Use `/ring:dev-cycle` to execute gated implementation through the 10 dev-cycle gates.

The roadmap pairs with `tasks.md` for per-task AC / DX references, `trd.md` for architectural invariants, and `prd.md` for business success metrics.

---

## 12. Update cadence

Refresh the roadmap at the end of Week 1 (after M1 lands). The comparison of actual hours for T1 + T2 + T8 against the 17h plan (7 + 8 + 2) tells us whether the 1.2x human validation multiplier was tuned correctly. Adjust the remaining forecast accordingly:

- If actual < planned: pull T11 stretch into scope or bring forward the declared delivery target.
- If actual > planned: flag the delta, reassess T9 risk posture earlier (start the T9a/T9b split discussion in Week 2 Mon rather than Fri).
- If actual ≈ planned: stay the course; declared delivery target holds at 2026-05-05.

Gate 4 pass criteria checklist (all green):

- All tasks scheduled with realistic dates (§4).
- Critical path identified and validated (§6).
- Team capacity 90% applied via the Gate 4 formula (§3).
- Period boundaries respected — continuous cadence; none to enforce (§2 note).
- Spill-overs identified and documented (§7).
- Parallel streams defined (§8, informational for solo).
- Risk milestones flagged (§9).
- Contingency buffer added (15% → 3 working days, §10).
- Formula applied step-by-step (§3).
