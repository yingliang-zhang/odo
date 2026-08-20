> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Auto-land Pipeline Stage Visibility (odo, 2026-08-19)

## Context

Two workstreams active in a new odo worktree: UI stream running, main stream showing "auto-land in queue". Investigation confirmed this was **not** stuck — the pipeline was inside its silent window (verify + panel produce no journal rows, so the GUI could only derive "queued"). Diff #5 landed automatically moments later (`bf784d3 odo: accept diff #5`).

The queued ambiguity motivated making the pipeline's in-flight stage visible.

## Key decisions

- **Reuse `review_action` journal rows, no new event types.** Daemon journals `auto_land_started{stage:verify|panel}` before `runVerifyGate` and before `reviewFanout`. Matches design-lock rule 1 (no new event types) and fulfills rule 10's reserved Phase 2 ("don't pretend to distinguish verify vs panel without daemon events").
- **No risk receipt on started rows.** Liveness is not an outcome; avoids polluting the autonomy Unrated bucket.
- **`auto_land_started` rows excluded from the distill prompt** (`foldExcludedReviewAction`) — distill consumes outcomes, not mechanism noise.
- **No transcript bubble** for started rows; the chip is the only surface (`todo_merge` precedent). LedgerPanel keeps raw rows.
- **Unknown stage values degrade to generic in-flight** for forward compatibility; pre-Phase-2 journals derive exactly as before.
- **Stage visibility is honest about free gates.** Mechanical gates (`run_errored`, protected path, `base_stale`) return in milliseconds with zero started rows → chip goes queued → blocked directly, never claims "running".
- **Refresh probe precedes started(verify)**, so rebase scenarios retain their existing in-flight hint. Each revise round re-enters `autoLand`, so rounds get stage visibility automatically.

## Code changes (landed as diff #6 → `c28a136 odo: accept diff #6`, 12 files, +244/−22)

| Layer | Change | Files |
|---|---|---|
| daemon | `journalAutoLandStarted` + two instrumentation points (after free gates → verify; after model parse → panel fan-out), carrying patch_sha16 | `internal/ipc/autoland.go` |
| daemon | `foldExcludedReviewAction` excludes started rows | `internal/ipc/server.go` |
| GUI | `PipelineState.stage` + `auto_land_started` derivation case; verbatim chip labels "verify running…" / "panel reviewing…" | `gui/src/pipeline.ts`, `types.ts`, `StatusBar.tsx`, `MessageBubble.tsx` |
| Contract tests | Happy-path journal order (started(verify) < started(panel) < moa_review < accept); zero started rows on free-gate block; stage derivation + unknown stage + verdict override; E2E label + no bubble | `autoland_test.go`, `learner_test.go`, `pipeline.test.ts`, e2e spec |
| Docs | Design-lock rule 10 marked done + Phase 2 amendment; README line | lock, README |

Verification: `go build && vet && test ./...` green (ipc 396s); `tsc --noEmit` clean; vitest 57/57; playwright pipeline-chip 10/10 + regression grep 16/16.

## Self-modification gate fired as designed

Diff #6 touched `internal/ipc/autoland.go` — one of the 9 `protectedGateFiles` (`server.go:3626`). Mechanical gate flatly rejected auto-land: journal seq 315 `auto_land_blocked{reason:protected_path, diff_id:6, actor:auto_panel, risk_class:["none"]}`. Rule from 2026-08-15 three-model consensus: a diff weakening the gate must never auto-land.

Human escape hatch used: user accepted manually; seq 348 `review_action{action:accept, diff_id:6}` with **no actor field** (human click, vs `actor:auto_panel` on auto-lands). Path pinned by `m6_test.go:696 TestHumanAcceptGateSourceAllowed`. Verified: main HEAD `c28a136`, diffs #6 = `accepted`, `base_sha == head_sha` (no rebase drift).

**Expected future behavior:** any edit to gate source files (autoland, autonomy, learner, review, settle, ledger, risk, contradiction, design_moa) will replay "blocked: protected_path → manual review" — a feature, not a fault.

## Diagnostic anchors going forward

- True stuck condition: no moa/auto-land lines in `daemon.log` + no `review_action` in journal + duration > ~25 min (legal silent-window ceiling = verify 10 min timeout + 900s/panel leg).
- Daemon restart `recover-pending-diffs` re-triggers all pending diffs.
- A bare "queued" lasting more than a few seconds is now genuinely anomalous, since verify/panel stages report as running.

## Open loops

- Wiki/ uncommitted learner memory writes in the main workspace — commit decision left to the user.