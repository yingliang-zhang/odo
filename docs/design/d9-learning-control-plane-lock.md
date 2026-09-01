# D9 Learning Control Plane — DESIGN LOCK (waves 3-6)

- Date: 2026-08-30. Anchored at odo HEAD `369f574` (D1-D8 + adoption-lock P1-P3 landed).
- Source: 4-leg blind-sealed design review (K3 `sudo/t9s/kimi-k3`, GLM-5.3
  `sudo/t9s/glm-5.3`, DSF `sudo/t9s/deepseek-v4-flash`, Sol `sudo/t9s/gpt-5.6-sol`,
  900s, `--thinking max`, repo-readable). Full leg artifacts:
  `/tmp/odo-d9-design/{k3,glm53,dsf,sol}/out.md`; the three in-repo long-form
  designs (`docs/design/learning-control-plane-d9.md` K3,
  `docs/design/learning-control-plane-d9-design.md` GLM-5.3,
  `docs/design/d9-learning-control-plane-design.md` DSF) are retained as
  appendices and superseded by THIS document where they conflict.
- Base doc: `docs/design/control-plane-hardening-lock.md` §D9 (lines 151-154).
- Doctrine: journal-first; append-only additive journal shapes (ADR-0002);
  zero LLM in gates; human-only `global_active` + `user.md` forever; evidence →
  measure → gate as three structurally separated steps.

## User rulings on the 4-leg divergences (2026-08-30, binding)

- **R1 — rollback semantics = DSF/Sol**: rollback operates on the CANDIDATE
  LAYER only (stage demotion, instant, fold-derived, zero memory.md writes).
  If a candidate had reached project_active (its rule already in memory.md
  through the receipted path), the rollback fold ADDITIONALLY emits
  `memory_update{layer:"memory", cause:"retract_candidate", rule, flag_seq,
  candidate, epoch}` — the existing D4 receipt shape — and the human resolves
  via `apply_memory` / `odo rules retract`. The daemon NEVER deletes the
  memory.md line itself. Two layers, no contradiction with D4. (K3's
  auto-write variant is rejected; DSF's resolution adopted verbatim.)
- **R2 — candidate freeze window = K3**: 3 MAIN-lane epochs (project-scoped
  cadence), same constant as the memory guard, window keys differ because
  scopes differ. Enforced at learner vet + candidate lint + stage-interrupt.
  Boundary fixture: rollback at epoch N ⇒ re-propose at N+1 rejected
  (`oscillation_guard`), N+4 free. (GLM-5.3's freeze-forever-on-2nd-rollback
  rejected — one misfire would permanently kill a rule idea.)
- **R3 — canary fraction = K3**: default 0.25 (prefs `learning_canary_fraction`),
  hard ceiling 0.5, 0 = canary disabled project-wide. Deterministic interleave
  `ordinal % M == 0` per lane (M = round(1/f), zero RNG state); assignment
  journaled BEFORE the run (`learning_cohort`); steer-continuations inherit
  the chain's cohort. Single canary slot (cohort purity; queued candidates
  journal `shadow_queued`).

## Corrections carried from the legs (all 3+ legs independently verified)

1. MemoryPanel does NOT show flagged rules (self-improving wave-4 never
   dispatched) — D9-W3 builds the first flag surface, not an extension.
2. learner/autonomy/settle are Tier-1 gate source (D1 made C0
   memory-prefix-only), NOT C0. Stage-actuation diffs route
   human-Accept-preferred (project rule); Tier-0 untouched.
3. `memory_replay.go` is a crash-recovery replayer, not a projector. Frozen
   replay reuses the rules-audit join + cohort receipts + `planMemoryApply`
   pure projection + paged reads.
4. Lock-text tension 0.4/0.5 resolved by R1 above and: `uses`/`cost` in
   candidate provenance are creation-time only; running counters are
   journal-fold-derived, never stored on the immutable row.

## Frozen design (authoritative; K3 doc §1-§7 as amended here)

> **AMENDED 2026-09-01** — replay checks f/g are DELETED; efficacy moves to
> the canary layer. See `docs/design/d9-lock-amendment-a1-fg-canary.md`
> (authoritative).

The K3 long-form (`docs/design/learning-control-plane-d9.md`) is the base
skeleton — densest file:line verification and the most complete pass
criteria. The following amendments from the other legs are merged in:

- **From GLM-5.3**: replay pass gains the "≥1 prevented-harm" requirement (an
  empty do-nothing candidate cannot pass by vacuity — zero caused-harm AND at
  least one counterfactual prevented-harm, friction ≤ 3× prevented-harm).
  The candidate grammar is closed and conservative-only: `when: predicate
  over journaled fields, then: route_human|block|advisory` — loosening is
  grammatically impossible; numeric gate mutation excluded (that circularity
  is what D1 exists to prevent).

  > **AMENDED 2026-09-01** — replay checks f/g are DELETED; efficacy moves
  > to the canary layer. See `docs/design/d9-lock-amendment-a1-fg-canary.md`
  > (authoritative).
- **From DSF**: candidate status and `uses` fold from the journal
  (`learning_stage` rows + canary receipt key), keeping candidates.jsonl
  content-addressing honest. `global_active` = folded receipt of a HUMAN's
  own promotion action (`odo learning promote --global`), never
  daemon-initiated, never writes user.md. The harmful-tuple 10-injection
  floor's rollback latency concern is answered with per-epoch measure
  cadence (checkpoint re-measure), explicitly rejecting a second faster
  trigger path. W3a adds the additive `verify_ms` journal key (verify
  duration is currently unjournaled — a real gap the legs surfaced).
- **From Sol**: the anti-replay-divergence pin is mandatory — same rule set,
  same journal slice, live mode vs replay mode MUST produce identical
  projections (unit test, fixture bytes pinned). The "perfect hallucination"
  failure mode (a rule effective only because it suppresses the error it
  should surface) is answered by the baseline-is-golden-set rule: baselines
  never include the current candidate chain (never-score-own-changes).

## Stage machine (locked)

```
candidate → shadow         lint + security + frozen replay ALL pass (3× learning_gate + learning_stage)
candidate → dropped        any gate fail (per-check evidence journaled)
shadow → canary            ≥3 main epochs aged + replay re-pass on grown slice + no harmful tuple + canary slot free
canary → project_active    paired cohorts ≥10 human outcomes; canary reject-rate ≤ live×1.25; harmful tuple
                           absent; taint ≤ live+5pp; additive-only delta (integer cross-multiplication)
canary → held_for_human    stats pass BUT delta carries retractions (D4 preserved)
held_for_human → active    apply_memory / odo rules retract / odo learning apply (HUMAN)
project_active → rolled_back  harmful tuple on candidate's own delta.add (R1 semantics)
project_active → global_active  odo learning promote --global ONLY (HUMAN, never writes user.md)
any → frozen              oscillation freeze (R2: 3 main-lane epochs)
```

## Wave slicing (one auto-land dispatch per wave)

- **W3 — pure observability (zero behavior change)**: `learning_episode` fold
  at distill tail; `learning_store.go` (jsonl writer, unconsumed); additive
  `verify_ms` + `run_usage` rows (fail-soft); `learning_status` IPC +
  `odo learning status` CLI; MemoryPanel **Learning** sub-tab with the
  never-landed flag display. All Tier-1.
- **W4 — lifecycle core**: candidate/lint/replay/stages/measure; the
  `autoApplyProposals` hook behind `learning_stages:` pref (default ON, off =
  legacy); human apply = jump to project_active; shadow checkpoints; canary
  seam + promotion; live-audit exclusion. Hook-file edits
  human-Accept-preferred.
- **W5 — rollback + freeze + never-score**: `learning_rollback` (R1 semantics),
  measure exclusions, GUI stage feed + human actions.
- **W6 — global promotion + hardening**: `promote --global` (stage row +
  prints rule line; never writes user.md), drop/apply CLIs, `learning_stall`
  advisory (aging without cohort minimums — surfaced, never auto-promoted).

## Failure-mode pins (12; sharp ones)

Rollback→re-propose loop → R2 freeze + integer-boundary fixture. Promotion
starvation → explicit minimums + `learning_stall`, never age-based promotion.
Self-reinforcing cohort → paired-cohort requirement (both ≥10).
Continuation misattribution → chain-bound cohort, stage-flip fixture keeps
first hash. jsonl tamper → fold validates hash chain, unresolvable ⇒ invalid +
transition refusal. Replay nondeterminism → Sol double-execution pin + fixed
fixture bytes. Float-trap boundaries → integer cross-multiplication
everywhere. Episode rows poisoning the fold guard → whitelist pin (episode-only
window = "nothing new"). Rollback over-reach → restore bounded to candidate's
own delta.add (R1 layer separation makes this structural). Perfect
hallucination → golden-set baseline (Sol). Vacuous candidate → GLM prevented-
harm requirement. Stage-stall → K3 `learning_stall` advisory.
