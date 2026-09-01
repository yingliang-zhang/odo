# D9 Lock Amendment A1 — replay f/g efficacy checks move to canary (2026-09-01)

Quad-blind review: K3/GLM/DSF/Sol = 4/4 ACCEPT_WITH_FIXES, all fixes
merged below. User ruling: Option 1 (2026-09-01). Supersedes the f/g
clauses of §2.3 in docs/design/d9-learning-control-plane-lock.md.
Source backlog: docs/design/d9-replay-stage-order-backlog.md (da4e311).

## Amendment 1 — replay pass criteria (§2.3 replacement)
Replay keeps ONLY deterministic hygiene: a (harm-tuple absence), b
(retract-intersection), c (rotation projection), d (growth caps), e
(Sol double-execution pin), h (loosened==0), provenance. Checks f
(anti-vacuity) and g (friction ≤ 3×prevented-harm) are DELETED from
replay. Empirical basis: both historical candidates died at f+g with
f measuring zero by construction (starved freeze pool; 14/16 outcomes
scoring-excluded in seq 18639); candidate 90ed446c was additionally
killed by d — hygiene demonstrably works at replay.

## Amendment 2 — f′ lands inside learningPromotionVerdict (§5.3 intact)
f′ = liveHarm ≥ 1, where liveHarm = m.Live.Rejects + m.Live.WeakRejects
(integer, no division). Evaluated ONLY after the paired-minimums floor
(canary ≥10 ∧ live ≥10 human outcomes). Insertion after the floor check;
the destructive harmful-tuple leg stays first. No separate canary gate:
§5.3's AST pin (gates consume the journaled measure struct only) makes a
parallel gate unlawful or duplicative; all inputs are already journaled
per epoch by learningCanaryMeasure. Per-check booleans ride the verdict
detail map (replay checks convention).
Semantic bound (lock text): f′ proves EXERCISE (the rule's harm class
exists in live traffic during the observation window), not PREVENTION.
Post-promotion efficacy backstops remain the harmful tuple + R1 rollback.
g is NOT transplanted: the locked canary-reject ≤ live×5/4 leg
(integer cross-multiplied, learning_measure.go:461-470) structurally
dominates any canary-side friction bound (K3 retirement argument,
GLM/DSF concur). One new journaled key: stage_epoch (canary-entry epoch,
additive, ADR-0002-safe) — needed for the grace rule.

## Amendment 3 — three drop exits (anti-squat; R3 single-slot resource)
1. efficacy_vacuity: floors met ∧ liveHarm=0 ∧ canary residence ≥
   learningShadowAgingEpochs (3 main-lane epochs grace) ⇒ canary→dropped.
   Vacuity at full sample is a MEASURED property of the rule under live
   injection (the do-nothing class this design exists to reject).
2. canary_starved: canary age > 2×learningStallMainEpochs (24 main
   epochs) with paired floors still unmet ⇒ canary→dropped, detail
   carrying m.Excluded (exclusion-rate visibility). This is the ONE
   sanctioned amendment to "stall is advisory-only, never auto-dropped"
   (W5 R-semantics): age-based DROP is not age-based PROMOTION; the
   promotion-starvation pin's operative half stands. Error asymmetry:
   wrong drop costs one re-proposal; wrong squatter costs all future
   candidates.
3. Both vacuous-drop causes write ZERO entries to the R2 freeze set
   (vacuity ≠ harmful; 4/4 explicit).
Stall-advisory fixes riding along: busy-but-vacuous candidates (floors
met, liveHarm 0) previously received NO advisory (the advisory armed
only below the floor) — the new drop exits subsume this hole; stall
advisory dedupe becomes epoch-keyed (re-cycle blind spot, Sol fix).

## Amendment 4 — seam-disabled gate (pre-existing bug, fixes now)
learningShadowCheckpoints must NOT flip shadow→canary when
learningCanaryFraction() <= 0 (GLM finding: stages:253-257 flips
unconditionally while learningCurrentCanary returns nil at fraction 0 —
a structurally permanent squatter with zero possible cohort outcomes).

## Amendment 5 — shared predicate pin
The live-harm attribution join used by f′ shares ONE predicate
implementation with the replay-era attribution join (no dual
implementation drift; the double-execution fixture extends to cover the
measure fold).

## Failure-mode pin addendum (to the lock's 12)
13. Vacuous squatter: closed by Amendment 3 (drop exits) + Amendment 4
    (seam gate).
14. f′-drop churn in calm projects: zero-reject eras will drop good
    procedural rules at full sample (both historical candidates were
    such rules). Accepted cost, named aloud: learner re-proposal is
    bounded, freeze-set untouched, journal visible. Repeat-vacuity
    window-freeze deferred to a future ruling (not required for
    correctness).
15. Check-a harm recall is cosmetic in starved projects (K3 bonus
    finding: ≥10-injection floor unreachable with 2 covered outcomes —
    pre-existing, absorbed by the lock's honesty clause §2.2).
