# Frozen Brief — Tri-Model Design: fix-INT Wave 1 (accept pipeline correctness)

## 1. Context

Odo (`~/Projects/odo`) is a personal Research Coding OS: Tauri 2 + React GUI driving OMP
CLI agents; the **Go daemon** (`internal/ipc`) is the single durable authority with an
append-only SQLite journal. Diffs produced by agent runs sit in a review queue
(`store.Diff`, status pending). Two resolution paths:

- **Human accept**: GUI click → `handleDiffAction(ctx, diffID, "accept", "")`
- **Auto-land** (M16/M18 settlement ladder): `autoLand(...)` runs mechanical gates →
  `.odo-verify` re-run in the run's worktree → unanimous MoA review panel →
  journals the verdict → calls the same `handleDiffAction` with `actor=autoActor`.

Design invariants you must preserve: append-only journal (evidence before action —
an unrecorded auto-accept is worse than none), fail-closed gates, `acceptMu`
(daemon-wide, one accept/reject at a time), patch-path-scoped rollback baseline
(`CapturePatchBaseline`/`RollbackPatchApply`), protected-path deny (`wiki/`, `.odo/`).

This is Wave 1 of the fix-INT batch (M17 audit P1 leftovers). Three items, all in
the accept/auto-land pipeline. Your designs become a DESIGN LOCK; a single K3
implements afterward with no design freedom.

## 2. Current state (verified by orchestrator at HEAD 72e6fe5 — read these first)

**A. Human accept has NO base-freshness check.**
`internal/ipc/server.go` `handleDiffAction` (~lines 1439–1560): under `acceptMu`
it checks protected paths, unmerged index entries, captures the rollback baseline,
then `git.ApplyDiff(s.projectRoot, d.PathOnDisk)`. It never compares the main
checkout HEAD against the diff's stored `BaseSHA` (`store.Diff.BaseSHA`, set at
`internal/ipc/server.go:~1293-1300` from the worktree's REAL HEAD). While a diff
sits pending, main HEAD moves (another accept lands, the user commits directly).
Two pending diffs commonly share one base: accepting the first invalidates the
second's base — but the second still applies (often cleanly via `--3way`), a
semantic mis-merge nobody attested.

**B. auto-land checks base freshness ONCE, hours of pipeline time before landing.**
`internal/ipc/autoland.go` `autoLand` (~lines 227–239): `base_stale` gate
(head != base → block) runs *before* the verify spend (`autoLandVerifyTimeout =
10 * time.Minute`, line 135) and *before* the review fanout (MoA panel — itself
many minutes). The land happens after all of it via `handleDiffAction`. A drift
between the early check and the land applies onto a tree the verify/panel never
attested. Note the check at line 231-239 reads HEAD, and `handleDiffAction` later
re-locks `acceptMu` — there is no atomicity spanning check→land.

**C. Review-leg read timeout is ~300s-class; max-thinking legs time out.**
`internal/moa/client.go`: `post()` derives per-request deadline via
`requestTimeout(maxTok)` = `baseRequestTimeout` (300s, line 52) +
`maxTok/genTokPerSecFloor` (120 tok/s floor). The review fanout
(`internal/ipc/review.go` / `reviewFanout`, called from `autoland.go:288`) marks
transport/timeout legs as infra (M18 `panelInfraLeg` at autoland.go:292, and
`reviewWithModel`'s timeout mark — see `internal/ipc/settle.go:~142`), which
fail-closes the round as `panel_infra`. Observed failure: max-effort review legs
on large diffs exceed ~5.5 min and the round dies as infra — this is what the
fix-INT item "bridge REVIEW 330s→900s" refers to (330s ≈ 300s + small token-floor
budget). There may also be a per-leg cap in the fanout itself — trace it; the
fix must cover every cap on the leg's critical path, not just the HTTP client.

## 3. The three items

**Item 1 — accept TOCTOU (A+B as one design problem).**
Design the race-free base-freshness guarantee for BOTH accept paths. The obvious
candidate — freshness recheck inside `handleDiffAction` under `acceptMu` — fixes
human and auto paths in one atomic spot; evaluate it and at least one alternative
(e.g., recheck at end of `autoLand` only + document residual race; or a
compare-and-swap land primitive). Decide the stale behavior per actor:
- actor=auto: block is settled law (m16) — keep, but where should the *final*
  check live so the block is truthful?
- actor=human (GUI click): hard-block a stale base too (the human resubmits after
  a rebase), or block-with-explicit-override? Consider what the GUI/journal must
  show. A stale-but-clean `--3way` apply is exactly the mis-merge no one attested.
Also resolve: does `BaseSHA == nil` (older diffs) change treatment? Fail closed
or grandfather?

**Item 2 — chained-auto-land base_stale recheck.**
This is Item 1's auto half, called out separately because the pipeline's drift
window is minutes-long. If your Item 1 design places the atomic check inside
`handleDiffAction`, say so explicitly for this item (same fix) and specify what
`autoLand`'s early check becomes (cheap pre-spend filter stays? change message?).
If Item 1 went another way, specify this item's race-free placement independently.
Journaled outcome must distinguish "stale at entry" from "went stale during
pipeline" (different operational meanings) — propose the reason strings/events.

**Item 3 — Review-leg timeout floor: 300s class → 900s class.**
Design where the knob lives and how it reaches every cap on the review-leg path:
constant vs `prefs.md` line (existing pattern: `omp_timeout:` in
`internal/adapter/settings.go`), plumb through fanout → client. Keep the
token-floor escalation math coherent (deadline = base + maxTok/floor — raising
base moves the floor, not the ceiling: max-budget legs still get their headroom).
Decide the value: 900s default? and whether the manual-review path (MoA button)
and auto-land path share the knob. No behavior change for distill
(`distillTimeout = 10m`, server.go:2211) unless you find it shares the same cap.

## 4. Constraints

- Minimal, surgical diffs. No redesign of the panel, verify gate, or settlement
  ladder. No new dependencies. Match existing Go style (comments are long-form
  and reason-heavy — keep that voice).
- Tests are part of the design: name the new test functions per item
  (existing harnesses: `autoland_test.go`, `server_test.go`
  `TestVisibleLoopAcceptRejectRestore`, `TestAcceptDoesNotSweepMainCheckout`;
  stale/accept races should be testable via the existing store/git fakes).
- Backward compatibility: pending diffs with `BaseSHA == nil` exist in the live
  journal; the GUI (`gui/src`) surfaces review queue states — flag any GUI-visible
  string you change. Contract changes (new block reasons, prefs lines) are allowed
  ONLY if the brief asked for them — they must be called out as contract changes.
- Do NOT touch: worktree lifecycle, memory layers, skills, the verify command
  allowlist env, or the `moa_fs_deny` semantics (separate wave).

## 5. Output format (required)

Per item (1, 2, 3):
1. **Root cause** — one paragraph, code-verified.
2. **Options** — ≥2 with trade-offs (for item 3, ≥2 knob placements).
3. **Recommendation** — picked option + why; exact symbols/files/lines to change;
   new journal events/reasons/prefs lines if any (marked CONTRACT CHANGE).
4. **Test plan** — exact test function names + what each asserts.
5. **Risks** — what your fix could break; how the tests catch it.
End with a **consolidated diff sketch** (file-by-file bullet list, ~15 lines).

## FINAL-MESSAGE CONTRACT (flash variant)

Your reply may take many tool calls — that is expected. But the session's FINAL
assistant message must contain the COMPLETE structured deliverable (all three
items, all 5 fields each, plus the diff sketch). Do NOT stop after dumping
intermediate evidence notes; if you finish gathering, keep writing inside the
same turn until the full report is in the final message. The final message must
start with "# Wave-1 Design Proposal".
