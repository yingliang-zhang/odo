Working...
# Blind review — D9 lock amendment: move replay f/g to the canary layer

## Verdict: **ACCEPT_WITH_FIXES**

The direction is correct and the factual claim checks out: replay f/g (`learning_replay.go:618-632`) gates on slice-wide behavioral evidence that a newborn candidate cannot have, while a–e+h are genuinely static/hygiene (`learning_replay.go:482-664`). Both historical f+g deaths with a–e green are the temporal-certainty signature described. The move is layer-matching onto existing paired-cohort machinery (`learning_measure.go:442-485`), not new mechanisms. Fixes required: f′/g′ land **inside** `learningPromotionVerdict` (not a parallel gate), a canary **slot-vacation demote** is added (stall advisory alone is insufficient), and the stall advisory's dedupe has a re-cycle blind spot. Details below.

---

## Q1 — Exact new home: inside `learningPromotionVerdict`, plus a tick-driven slot vacation

**Home: inside `learningPromotionVerdict` as additional promote conditions.** Not a separate canary-stage gate with own journal rows:

- §5.3 three-step separation is load-bearing and enforced structurally: gates take the measure struct, never `[]store.Event` (`learning_measure.go:9-16`, AST-pinned in `learning_measure_test.go`). A second canary gate would need a second measure row family or would bypass the separation.
- The evidence f′/g′ need is **already journaled**: `computeLearningMeasure` tallies per-rule `Injections/Accepts/Rejects/WeakRejects` into `m.Rules` (`learning_measure.go:72-82`, folded at `learning_measure.go:216-371`), and `learningCanaryMeasure` journals the full measure row every epoch (`learning_stages.go:458-468`). A separate gate would duplicate this.
- The tick already dispatches on the verdict's four outcomes (`learning_stages.go:475-502`); f′/g′ misses ride the existing `""` branch.

**Stay or drop for a vacuous-at-canary candidate:** `""` (stay) while evidence is accumulating — consistent with the locked posture that stats misses are never drops (`learning_measure.go:469` comment; drop is reserved for the canary-scoped harmful tuple, `learning_measure.go:448-455`).

**Slot squat is real and the W5 stall advisory does NOT stop it.** `learningCanaryMeasure`'s default branch journals `learning_stall` once (`learning_stages.go:490-502`), but `journalLearningStall` dedupes per `(hash, stage)` **forever** (`learning_stages.go:596-613`) and never acts. Meanwhile `learningShadowCheckpoints` refuses to promote anything while any candidate folds to `canary` (`learning_stages.go:190-195`) — one quiet-project candidate permanently starves the entire pipeline. Fix: **canary→shadow demote (`cause:"canary_stalled"`)** in the tick's default branch when `epoch − canary-entry-epoch > learningStallMainEpochs` **and** `m.Canary.Outcomes < learningPromotionMinOutcomes`. Demotion is structurally free: canary reverts are "stop injecting" (`learning_canary.go:28-29` — memory.md never written before `project_active`, in-flight chains finish on their bound cohort). The demoted candidate re-enters the existing `shadow_queued` path (`learning_stages.go:239-249`). This is not age-based promotion — it is age-based *vacation of a scarce resource*, which does not touch the promotion-starvation pin.

## Q2 — g′ reformulation at canary: exact integer inequality

The original g (`friction ≤ 3 × prevented-harm`, `learning_replay.go:631-642`) is unreproducible live: prevented harm is a counterfactual (the reject that didn't happen), observable only in replay projection. At canary, the honest differential uses the same shape as the harmful tuple's rate leg (`rules_audit.go:570-577`) with the live cohort as baseline — **per candidate-add rule row, at 3× (tighter than the tuple's 2× flag but with no injection floor, so it fires before the tuple can)**:

```
g′ (friction pre-floor guard): for every r in m.Rules:
    (2·r.Rejects + r.WeakRejects) · m.Live.Outcomes
        ≤ 3 · (2·m.Live.Rejects + m.Live.WeakRejects) · r.Injections
```

Integer cross-multiplied throughout (division-free; the exact `rules_audit.go:570-571` float-trap precedent — `2×0.15 ≠ 0.3` at the boundary — and the same convention as the promotion reject leg, `learning_measure.go:461-466`). Live-baseline denominators are safe: g′ is evaluated only after the paired minimums (`learning_measure.go:458-460`), so `m.Live.Outcomes ≥ 10` — no zero-denominator branch, unlike the original g whose denominator-0 case is precisely the bug being fixed. Violation ⇒ `""` (stay + stall advisory), never drop; the harmful tuple remains the only drop path at canary.

f′ at canary is necessarily weaker than replay-f and should be stated as such: **exercised floor** — `Σ over m.Rules of (Accepts+Rejects+WeakRejects) ≥ 1`. The canary cohort's *aggregate* outcomes are already floored at 10 by the paired minimums, but those can be outcomes on conversations that never touched the candidate's rules; f′ requires evidence the rule itself fired. "Prevented harm" semantics are unrecoverable live — accept the semantic weakening explicitly in the lock text rather than pretending the canary measures prevention. (Replay's `PreventedHarm`/`Friction` counters, `learning_replay.go:618-630`, should keep being computed and journaled in `shadow_checkpoint` metrics (`learning_stages.go:204-206`) as non-gating observability — the signal is deterministic and worth keeping; only the gate flips.)

## Q3 — Shadow entry with f/g removed: existing criteria are sufficient

Shadow→canary requirements today (`learning_stages.go:228-256`): replay re-pass on the grown slice, `mainEpoch − candidate epoch ≥ learningShadowAgingEpochs (3)`, R2 freeze-set interrupt, single free canary slot. The W4 seam (fraction 0.25 default, `M = round(1/f)`, `ordinal % M == 0` chain-root interleave, pre-run `learning_cohort` journaling, steer-continuation inheritance) is confirmed at `learning_canary.go:8-16, 53-85, 264-294`. These are sufficient — **with one amendment, the Q1 slot-vacation demote**, because the slot-free criterion is only sound if a stalled occupant eventually vacates. Two non-blocking notes: (a) with f/g out of the pass set, `learningShadowCheckpoints`' metrics map still reports `prevented_harm`/`friction` (`learning_stages.go:203-206`) — keep, relabel as observability; (b) the replay nondeterminism pin (e) and hygiene a–d+h are unaffected; the "never-score exclusions starve the freeze cohort" compounding concern dissolves for gating — the only evidence-dependent replay check remaining is check a, which is a fail-detector where zero evidence correctly passes (`learning_replay.go:482-503`).

## Q4 — Rollback interaction: no change to what rollback sees; one accepted exposure

`learningRollbackTargets` reads `m.Rules[].Harmful` from the measure's baseline pool (`learning_measure.go:487-499`); the tuple is computed identically regardless of where f/g gate. R1 layer separation (candidate-only demotion, restore bounded to `delta.add`) is untouched. New exposure, accepted: more candidates now reach `project_active` (f/g no longer culls at replay), so the harmful tuple becomes the *sole* automatic efficacy backstop post-promotion. That is exactly the locked design's intent ("replay is hygiene-only by design; canary stats decide", K3 §7 row), and rollback latency is bounded by the per-epoch re-measure cadence already locked. No new rollback failure mode. The one worth pinning: a candidate satisfying f′ on a single fluke outcome can promote; the tuple floor (≥10 injections) is the only recall net. That matches the existing risk posture of the promotion gate and needs no new mechanism — but the lock text should say f′ is an *exercised* floor, not a harm-prevention proof.

## Q5 — Lock text (minimal)

**§2.3 (pass-criteria list) — replace the f/g rows:**

> **D9-amend (2026-09-01, user ruling Option 1):** replay keeps a–e + h + provenance only. Former checks f (≥1 prevented-harm) and g (friction ≤ 3× prevented-harm) are REMOVED from the replay gate — they measure behavioral efficacy that only accumulates while a rule is live-injected; at replay time a newborn candidate is vacuous by temporal certainty, not defect (both historical candidates, epochs 44+45, died at f+g with a–e green; journal 18639, 18927-28). f/g move to the canary layer as promote conditions f′/g′ inside `learningPromotionVerdict` (§3.4). `PreventedHarm`/`Friction` counters remain computed and journaled in `shadow_checkpoint` metrics as non-gating observability.

**§3.4 (promotion predicate) — append:**

> - **f′ exercised floor**: the candidate's add rows carry ≥1 resolved human outcome in the canary cohort (Σ `m.Rules` outcomes ≥ 1). A rule that never fired cannot promote. This is an exercised floor, not a harm-prevention proof — the counterfactual is unobservable live.
> - **g′ friction pre-floor guard**: for every add rule row, `(2r+w)·Live.Outcomes ≤ 3·(2LR+LW)·rule.Injections`, integer cross-multiplied (`rules_audit.go:570-577` precedent). Violation ⇒ stay + stall, never drop (drop remains harmful-tuple-only). Evaluated only after the paired minimums, so no zero-live-denominator branch exists.
> - **Canary slot vacation**: canary aging past `learningStallMainEpochs` with canary outcomes below `learningPromotionMinOutcomes` demotes canary→shadow (`cause:"canary_stalled"`); the candidate re-queues via `shadow_queued`. Age-based vacation of the single slot (R3), never age-based promotion.

**§5:** no structural change — f′/g′ read `m.Rules`, which is already exclusion-filtered fail-closed by the shared predicate (`learning_measure.go:18-24`, `learning_measure.go:279-284`); the never-score starvation note becomes moot for gating. Coherence touch-up: reconcile `learningStallMainEpochs = 12` (`learning_measure.go:55`) vs K3 §6's "> 30" (`learning-control-plane-d9.md:296`) — implementation says 12; the doc should say 12.

## Q6 — Pins: one rewritten, two new

- **Rewrite** the "Vacuous candidate → GLM prevented-harm requirement" pin: → *Vacuous candidate → canary exercised floor (f′) + slot-vacation demote; replay never gates on live-only evidence.* The current pin, taken literally, is the bug being amended.
- **New pin — slot squat:** fixture: canary candidate with 0 canary outcomes aged past the stall floor ⇒ demoted to shadow, slot freed, sibling promotes next checkpoint; stall advisory present. Without this the amendment converts a replay death into a pipeline-wide livelock.
- **New pin — stall dedupe across cycles:** `journalLearningStall` dedupes per `(hash, stage)` forever (`learning_stages.go:596-613`); after a `canary_stalled` demote and re-promotion, a second stall is **silent**. Pin: dedupe key includes the entry epoch (or resets on stage transition).
- Not weakened: "Metric gaming" (replay hygiene-only, canary decides) is *strengthened* — the layer mismatch was the gaming surface. "Float-trap boundaries" extends verbatim to g′.

---

## Sharpest failure modes in the amended design

1. **Canary slot squat (must-fix in the amendment, not after):** without the slot-vacation demote, moving f/g to canary relocates the vacuity wall from "candidate can't advance" to "one candidate blocks every other candidate forever" — strictly worse, since `learningShadowCheckpoints` hard-refuses on an occupied slot (`learning_stages.go:190-195`). The stall advisory is visibility-only and its dedupe goes quiet after one cycle.
2. **f′ semantic gap:** canary f′ proves *exercise*, not *prevention*; a candidate can promote on 10+10 paired outcomes where its rules fired once and did nothing causal. Accept and document; the harmful tuple + R1 rollback remain the only efficacy backstops post-promotion. Do not let the lock text imply prevention is still measured.
3. **Demote/re-promote churn:** a candidate in a permanently quiet project cycles shadow→canary→(12 epochs)→shadow, generating journal noise and re-tripping the dedupe hole. Bounded and harmless (zero memory writes — canary never touches memory.md, `learning_canary.go:28-29`), but the epoch-keyed stall dedupe (fix 2 above) is what keeps the surface honest.
