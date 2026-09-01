Working...
All six questions answerable from the lock, the implementation, and the journal. Evidence gathered; no files modified.

---

# VERDICT: **ACCEPT_WITH_FIXES**

Option 1 is directionally correct and layer-matching, not new machinery: the promotion verdict (learning_measure.go:442-485) already consumes the paired cohorts whose tallies are the only honest substrate for f/g, and §5.3's structural separation (learning_measure.go:9-16, gate consumes the journaled measure struct only, AST-pinned) makes `learningPromotionVerdict` the only lawful home. But the amendment as naively stated ("move f/g to canary") has a hole the questions themselves anticipate: as pure promote-conditions returning `""`, a vacuous candidate squats the single canary slot (lock R3, learning_canary.go:171-228; learning_stages.go:189-195) **forever** — the W5 stall advisory never moves a stage (learning_stages.go:592-595). ACCEPT conditioned on the five fixes in Q1/Q2/Q3/Q4, drafted in Q5.

**One factual correction to the brief (evidence, journal seq 18927):** the epoch-45 candidate (`90ed446…`) did **not** have a-e all green — `checks.d = false`, `growth_max_bytes: 655` (> +512B cap), alongside `f/g = false`. Only the epoch-44 candidate (`dc9565…`, seq 18639) died on f/g alone (`a-e,h,provenance` all true, `prevented_harm: 0`, `friction: 2`). The temporal-certainty argument stands for both (f=false, g=false, prevented_harm=0 in both gate rows), but Option 1 would *not* have saved the epoch-45 candidate — it would still be dropped at replay d. The amendment fixes the class, not that instance; the lock text should not claim otherwise.

A sharpening of the verified problem worth recording: as implemented (learning_replay.go:618-631), f/g at replay are **candidate-independent traffic lotteries** — `covered` is every slice outcome resolving to a live snapshot (minus canary/scoring exclusions), tallied by kind, with no delta dependence. Whether a newborn candidate passes f is a property of the epoch's reject traffic (and of the exclusion rate — 14 of 16 outcomes scoring-excluded in seq 18639), not of the candidate. Both historical candidates died at the *candidate→shadow creation gate* (journal seq 18928: `from:"candidate"`, `gates_failed`), where zero exposure exists by construction. This is stronger than "vacuity at birth": f/g at replay cannot measure the candidate at all.

---

## Q1 — Exact new home

**Inside `learningPromotionVerdict` (learning_measure.go:442) as additional legs, not a separate gate.**

1. **§5.3 makes a separate canary gate unlawful or duplicative.** Gates consume the journaled measure struct only — "the evidence→gate shortcut is unrepresentable in code (the AST pin in learning_measure_test.go)" (learning_measure.go:9-16). A replay-style canary gate would either read raw events (violates the pin) or re-consume the identical measure row — two gate families, one decision, plus a new journal action shape for zero information.
2. **The inputs are already journaled per epoch.** `learningCohortMeasure` carries both cohorts' `Rejects/WeakRejects/Accepts/Outcomes` (learning_measure.go:59-67, 97-107), journaled every epoch at learning_stages.go:458-468. Zero new evidence plumbing — this verifies the consolidator's note.
3. **Journal surface:** per-check f′/g′ booleans ride the verdict `detail` map (the replay gate row's `checks` convention, learning_replay.go:188) and land in the existing measure row + any drop stage row's detail (learning_stages.go:479-489). No new action shape.
4. **Insertion point:** after the paired-minimums check (learning_measure.go:456-459), so f′/g′ bite only at full sample; the destructive harmful-tuple leg stays first (learning_measure.go:448-455).

**Vacuous-at-canary = stay or drop? Both, split by the floor.** Below the paired minimums (either cohort < 10, learning_measure.go:458-459): **stay** (`""`) — vacuity below the floor is still a clock artifact, exactly like every other stats miss ("stats miss: stay canary", learning_measure.go:469). At/above the floors: **drop** (new cause `"vacuous"`) — once both cohorts carry ≥10 human outcomes, the window is the design's own full sample; zero prevented harm then is a measured property *of the rule under live injection*, which is precisely the do-nothing class GLM's amendment exists to reject (lock lines 61-63). This is the asymmetry that makes Option 1 sound: at replay, vacuity precedes evidence (unmeasurable); at canary with full sample, vacuity survives evidence (measured).

**What stops a permanent squatter — three bounds (fixes 1-3):**
- **Fix 1 (full-window drop):** floors met + f′ fail ⇒ `canary→dropped` (new cause `vacuous`), not `""`. Without this, Option 1 is strictly worse than today: currently a neutral-stats candidate eventually *promotes* (the predicate has no efficacy axis — learning_measure.go:461-484 is all no-worse legs) and frees the slot; with f′ as a silent promote-condition it never would.
- **Fix 2 (residency cap):** canary age > 2×`learningStallMainEpochs` (24 main epochs; constant at learning_measure.go:55) with paired floors still unmet ⇒ `canary→dropped` (cause `canary_starved`, detail carrying `m.Excluded`). This is the one sanctioned amendment to "never auto-dropped" (learning_stages.go:593-595, cmd_learning.go:44) — justified by R3's single-slot resource (lock 34-39) and the error-cost asymmetry of a conservative-only grammar: a wrong drop costs one re-proposal; a wrong squatter costs all future candidates. Age-based *drop* is not age-based *promotion*; the promotion-starvation pin's operative half (lock 119-120) is untouched.
- **Fix 3 (seam-disabled gate):** `learningShadowCheckpoints` must not flip shadow→canary when `learningCanaryFraction() <= 0` — today the flip proceeds (learning_stages.go:253-257 has no fraction check) while `learningCurrentCanary` returns nil (learning_canary.go:177-179), so the candidate enters canary and *can never accrue one cohort outcome* — a structurally permanent squatter class that Fix 1 cannot reach (floors never met) and Fix 2 only kills 24 epochs later.
- Human `odo learning drop` (cmd_learning.go:19, 268-269) remains the final valve.

## Q2 — g′ at canary: the exact integer inequalities

Use the paired differential, not the original freeze-cohort form. The original g (learning_replay.go:632, `Friction <= 3*PreventedHarm`) had a within-cohort denominator; at canary the live contrast exists, so prevented harm is definable as *live weighted-rejects the canary cohort avoided* — the only formulation that is candidate-dependent (the replay form measured the slice, not the candidate; see above).

Notation from `m learningCohortMeasure` (learning_measure.go:97-107), the audit's weighting (learning_measure.go:463-464; rules_audit.go:559):

- `cr := 2·m.Canary.Rejects + m.Canary.WeakRejects`, `lr := 2·m.Live.Rejects + m.Live.WeakRejects`
- `lc := m.Canary.Outcomes`, `ll := m.Live.Outcomes`, `ca := m.Canary.Accepts`

**P := lr·lc − cr·ll** — integer, division-free. `P/ll` is the prevented weighted-reject count in canary-equivalent volume (live-rate expectation over the canary cohort minus what the canary actually produced), so both legs stay count-level:

- **f′ (anti-vacuity): `P ≥ ll`** — at least one prevented weighted-reject.
- **g′ (friction): `ca·ll ≤ 3·P`** — friction ≤ 3× prevented harm, cross-multiplied; at the minimal f′ pass (`P = ll`) this is exactly the original `ca ≤ 3`, preserving GLM's 3:1 at unit scale.

Float-trap guard: both are pure integer products — no division, no float64 anywhere, matching the precedent family (rules_audit.go:570-577's cross-multiplied flag legs with the explicit float-trap comment; learning_measure.go:40-45's N/D constants "keeps the comparison division-free"; the taint leg's mixed-unit cross-multiplication at learning_measure.go:474-476). Nit: the float-trap citation in learning_measure.go:40 and the brief say rules_audit.go:553-556; the actual comment sits at rules_audit.go:570-573 — harmless drift, fix opportunistically.

Two consequences the lock must own:

1. **The 5/4 reject-rate leg becomes dead code.** f′ passing (`lr·lc ≥ cr·ll + ll`) implies `4·cr·ll < 5·lr·lc`, so the no-worse leg (fail condition at learning_measure.go:465-466) is unreachable for any candidate that reaches it. Recommend **removing it from the predicate** (clean cutover) and keeping the counts journaled in the measure row — they already are (learning_stages.go:463-464). If retained, mark it subsumed in the lock text; a dead predicate in a locked stage machine invites false confidence.
2. **Posture change: full-sample non-improvers now drop.** Today a full-sample canary 1.3× worse than live stays (`""`, learning_measure.go:469). Under f′, `P < ll` ⇒ drop. This is forced by anti-squat and consistent with the conservative grammar — but it is a behavior change beyond "moving" f/g and must be stated in the lock, not smuggled.

Optional (recommend, cheap): tighten f′ to `P ≥ 2·ll` — see failure mode 2. **Reverts:** replay-f counted human reverts (learning_replay.go:390-406, 630); `learningCohortStats` has no Reverts field (learning_measure.go:59-67) and revert→cohort attribution (which chain's block was in play) is not in the measure fold. Recommend dropping reverts from f′ (the differential subsumes the signal) and noting the residual gap in §5: a canary *accept* later reverted by the human counts as friction, not harm — attribution machinery exists (learning_canary.go:137-169) if the lock later wants it.

## Q3 — Shadow entry

Confirmed from learning_stages.go / learning_canary.go. Shadow→canary requires: checkpoint replay re-pass on the grown slice (learning_stages.go:200, 221-227), age ≥ `learningShadowAgingEpochs = 3` (learning_stages.go:155-157, 228), no R2 frozen-text hit (learning_stages.go:235-238, fold at learning_measure.go:501-506), single canary slot free (learning_stages.go:189-195, 239-249; the slot fold at learning_canary.go:171-228; W4 seam comment learning_canary.go:2-6 is moot post-W5), one flip per tick (learning_stages.go:257). W4 seam parameters verified: fraction default 0.25/ceiling 0.5/0=disabled (learning_canary.go:53-72), `M = round(1/f) ≥ 2` (learning_canary.go:74-85), lane-ordinal interleave `ordinal % M == 0` with chain-root counting (learning_canary.go:264-282, learningIsChainRootSend learning_canary.go:91-110), steer/retry inheritance (learning_canary.go:137-169, 284-294).

**Sufficient with one amendment (Fix 3) plus acceptance of a scope shift.** With f/g removed, `computeLearningReplay` shrinks to a-e+h+provenance (learning_replay.go:29-49) and the shadow stage becomes pure hygiene aging — which is the amendment's intent: efficacy is unmeasurable while inert (K3 §2.2's epistemic honesty, learning-control-plane-d9.md:160). The checkpoint's remaining teeth are real (a: harm-adjacency on accumulating rejects; d: budget vs grown base — the epoch-45 candidate demonstrates d bites). The one gap: no fraction>0 gate on the flip (Fix 3). Also note the unverifiable fast-path at learning_replay.go:383-385 hard-codes the check-name list `{"a","b","c","d","f","g","h","provenance"}` — removing f/g from the gate requires shrinking that list too (implementation nit for the wave, worth naming in the lock's W4/W5 notes).

## Q4 — Rollback interaction

**No change to what rollback can see.** `learningRollbackCheck` computes its own measure (`since` = stage→project_active, learning_rollback.go:51-53) and reads targets via `learningRollbackTargets` = `m.Rules[].Harmful` (learning_measure.go:491-499; harmful tuple computed in the measure at learning_measure.go:404-410 over the canary∪live baseline, learning_measure.go:386-396). It never consults `learningPromotionVerdict`; f′/g′ live entirely inside the verdict. The R1 two-layer mechanics (candidate-layer marker + D4 `retract_candidate` receipts, learning_rollback.go:88-133) are untouched.

New failure modes and the fix:

- **Fix 4 (vacuous-drop oscillation):** R2's freeze set is fed by rollback/harmful-drop rows (learning_rollback.go:88-90 "the R2 freeze set reads this row"; enforcement points: learner vet, candidate lint, stage interrupt — learning_stages.go:232-238, lock 28-33). A vacuous full-window drop is *not* harmful evidence, so if it doesn't freeze, a `drop → re-propose → 3 epochs → drop` loop burns the canary slot every cycle — exactly the loop shape R2 exists to bound (boundary fixture: rollback at N ⇒ N+1 rejected, N+4 free, lock 30-32). **Extend the freeze fold to vacuous-drop texts, same 3-main-epoch window.** Cost: a starved-window drop (see FM3) locks re-proposal for 3 epochs — acceptable, and the drop detail must carry `m.Excluded` so a human reviewing `list --stalled` can distinguish "starved window" from "useless rule".
- Post-promotion, f′ evidence confers nothing: a noise-promoted harmless-but-useless rule never meets the harmful tuple and therefore never rolls back — it sits in memory.md indefinitely. That is failure mode 2, not a rollback defect; rollback's blindness to non-harm is by design (R1 targets only the candidate's own harmful rows).

## Q5 — Lock text amendment (minimal)

**§2.3 (replay pass criteria — K3 doc lines 162-175, adopted by the lock):** rows a, b, c, d, e, h, provenance unchanged; delete f/g rows; append one line: *"f/g (anti-vacuity, friction) are REMOVED from replay and re-sited at the canary promotion predicate (§3.4): prevented-harm evidence structurally cannot exist at candidate-creation time — the candidate is not live-injected (journal seq 18639/18927: a-e green or hygiene-fail independent of f/g, prevented_harm 0 both)."* Also amend lock lines 61-63 (the GLM clause "replay pass gains the ≥1 prevented-harm requirement…") — this is the sentence the amendment *relocates*; leaving it intact contradicts the new §2.3.

**§3.2 (canary cohort):** no mechanical change; add the flip guard: *"shadow→canary requires `learning_canary_fraction > 0` (a flip with the seam disabled strands the slot with zero evidence accrual)."*

**§3.4 (promotion predicate — K3 doc lines 213-221, lock stage-machine line 90-91):** after the paired-minimums bullet, insert:

> - f′: `lr·lc − cr·ll ≥ ll` — ≥1 prevented weighted-reject vs the live contrast (weights: `2·rejects + weak`, the audit convention; integer cross-multiplication, the float-trap precedent),
> - g′: `canary_accepts·ll ≤ 3·(lr·lc − cr·ll)` — friction ≤ 3× prevented harm,
> - full-sample f′/g′ failure ⇒ `canary → dropped` (cause `vacuous`) — never a silent stay; the slot is bounded,
> - canary residency > 2× the stall floor without paired minimums ⇒ `dropped` (cause `canary_starved`) — the single age-based-drop exception, R3 single-slot resource,
> - the `canary ≤ live×5/4` reject-rate leg is subsumed by f′ and retired from the predicate (counts remain journaled in the measure row).

**§5 (never-score):** unchanged in substance — the measure already excludes auto/other-canary/scoring-excluded from both legs (learning_measure.go:18-24, 299-313; learning_scoring.go:34-43). Two touch-ups: (i) note the revert-attribution gap (reverts no longer feed any efficacy leg; canary-accepts-later-reverted count as friction); (ii) note that exclusion rates are journaled per measure (`excluded`) and must ride vacuous-drop detail.

**Failure-mode pins list (lock 117-130):** reword pin 9 "Vacuous candidate → GLM prevented-harm requirement" to "…measured at canary on the paired differential; full-window vacuity ⇒ drop"; amend the promotion-starvation pin to name the residency-cap exception; add pins: *canary slot squatter → full-window drop + residency cap; disabled-seam stranding → fraction gate on the flip; vacuity-drop oscillation → R2 freeze extension.*

## Q6 — Pins

No existing pin is weakened in its operative direction; three are strengthened, one gains a scoped exception, three are new:

- **Strengthened:** vacuous-candidate pin (from structurally-unmeasurable at replay to measured-at-canary); self-reinforcing-cohort pin (f′ demands strict improvement, strictly stronger than both-≥10); float-trap pin (two new division-free inequalities in the same family).
- **Exception:** promotion-starvation pin keeps "never age-based promotion"; the residency cap adds the named age-based-drop exception. The W5 comment "never auto-promotes, never auto-drops" (learning_stages.go:593-595) and the W6 CLI text (cmd_learning.go:44) need the same scoped amendment.
- **New:** slot-squatter pin (Fix 1+2), disabled-seam pin (Fix 3), vacuity-oscillation pin (Fix 4).
- **The W5 stall advisory alone is NOT sufficient** for the vacuous squatter — it is explicitly advisory-only, "NEVER auto-promotes, never auto-drops" (learning_stages.go:592-595), rendered as a listing marker (cmd_learning.go:106-112). It is the visibility layer under Fixes 1-2, never the bound.

---

## Sharpest failure modes in the amended design

1. **Neutral-rule extinction.** f′ demands the canary be *strictly* better than live by ≥1 weighted-reject. Any rule whose value is not demonstrable as reject-prevention at a 10+10 sample — including rules that raise accept-rate or taint-profile without preventing a single reject — can never promote and drops at first full sample. The promoted population is biased to harm-preventers by construction. That is a coherent reading of GLM's amendment ("an empty do-nothing candidate cannot pass"), but it must be a *conscious lock ruling*, because it silently retires the accept-rate/tait-quality value axis the no-worse legs were built to admit. If unwanted, the fix is one OR-arm (canary accept-rate ≥ live × integer factor, cross-multiplied in the same style) — still integer, still paired; recommend deciding now, not discovering in epoch 50.
2. **One-unit noise, asymmetric cost.** The harm side of the machine needs 10 injections / 3 rejects / 3 distinct conversations / 2× baseline (rules_audit.go:94-97; learning_measure.go:404-408); the efficacy side as drafted needs a single differential unit (`P ≥ ll`) — one noisy live reject flips f′ at the floor. A false promote lands a useless rule in memory.md, and — unlike a harmful rule — nothing ever removes it (rollback fires only on the harmful tuple, learningRollbackTargets learning_measure.go:491-499). Recommend `P ≥ 2·ll` (≥2 prevented) before relying on it; per-rule prevention attribution is *not* available in the measure (the candidate's rule rows pre-promotion tally canary-cohort outcomes only, learning_measure.go:355-370), so conversation-distinctness is not a cheap alternative — the margin is.
3. **Exclusion starvation carries over to the live leg.** The same never-score predicate that zeroed the replay pool (seq 18639: `scoring_excluded: 14` of `outcomes: 16` — 87.5%) also prunes the live cohort at canary (learning_measure.go:299-313, fail-closed on unreadable patches, learning_scoring.go:51-69). In gate-source-heavy epochs `lr → 0`, making f′ unpassable at full sample (a clean live cohort is vacuous-adjacent: nothing existed to prevent) and stretching floor-fill time into the residency cap. Mitigations already required above: `m.Excluded` tallies in every vacuous/starved drop detail, and the residency budget (24 epochs) must tolerate the fill time of a realistic exclusion rate — otherwise the amended machine churns candidates in learning-plane-heavy projects, the exact traffic profile that produced epochs 44-45.
