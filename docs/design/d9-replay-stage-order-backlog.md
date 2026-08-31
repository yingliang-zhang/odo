# D9 backlog: replay gate stage-ordering tension (audit #5)

Date: 2026-08-31. Source: quad-model architecture audit (Sol + DSF legs,
K3/GLM corroborate). Status: OPEN — needs design-lock amendment, not an
in-place tweak (settled semantics, protected by lock §2.3).

## Verified facts (first-hand, journal seq 18639)
Candidate dc9565cb (the project's first-ever candidate, epoch 44) failed
replay on checks f+g while a-e ALL PASSED:
- f=false: "no prevented harm (vacuous candidate — zero reject/revert
  evidence in the covered slice)" — prevented_harm=0 over 6,392 slice
  events / 47 sends / 16 outcomes / 1 cohort.
- g=false: "friction 2 exceeds 3× prevented harm (0)" — with denominator
  0, g is structurally unpassable whenever f fails.

## The tension
Replay's charter (lock §2.3) is deterministic hygiene: a (no harm tuple),
b (no retract-intersection), c (no third-party eviction), d (growth caps),
e (Sol double-execution pin). Those are DECIDABLE at candidate birth.
f (anti-vacuity: ≥1 prevented harm) and g (friction ≤ 3×prevented-harm)
measure BEHAVIORAL EFFICACY — evidence that only accumulates while the
rule is LIVE and injected. A candidate cannot have prevented harm before
it has ever been injected; vacuity at replay time is a temporal certainty
for every first-generation candidate, not a defect signal.

Compounding (DSF leg): the never-score exclusions (lock §5) shrink the
evidence pool further, so even future candidates from gate-adjacent
epochs face a starved cohort.

## Design options (for the next D9 design wave — quad-blind review)
1. Move f/g to the canary stage (shadow/canary measures efficacy; replay
   keeps a-e hygiene only). Cleanest layer match; requires the freeze
   cohort to also bind the canary observation window.
2. Vacuity-tolerant replay: f/g evaluated but non-fatal for first-N-epochs
   candidates (stall advisory instead of drop — R-semantics "surfaced,
   never auto-promoted" already covers advisory drops).
3. Provenance-based grandfather: candidates whose source flag rows show
   pending-harm evidence (harmful flag exists, cohort thin) get a
   provisional pass with mandatory re-eval at promote time.
Recommendation (consolidator note, no vote): option 1 matches the layer
doctrine; option 2 is the minimal change. User ruling required.

## Why not in-place
The replay pass criteria are lock §2.3 text; changing them is a design
amendment (quad-blind + user ruling), not a bug fix. The first candidate
drop is CORRECT per current lock — this backlog item questions the lock,
not the implementation.
