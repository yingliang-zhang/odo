> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# W6 v2: re-apply archived W6 diff + context-panel-tabs spec recalibration — all 7 gates green

## Context

- Follow-up run after the prior attempt's auto-land was blocked (`verify_failed`) and the review rejected; the agent's own fresh worktree (cut from HEAD) was empty by design.
- Task contract: one diff — (1) re-apply the archived, suite-validated W6 implementation, (2) recalibrate one stale e2e spec. Stage only in the own run worktree; leave it dirty; zero commits; sibling worktrees read-only; do NOT touch `ContextPanel.tsx`/`contrib.ts` component logic.

## Key decisions

1. **W6 re-apply via `git apply --3way`** of `/Users/yingliangzhang/Projects/odo/.odo/diffs/6a953f61-596d27e1cbba.diff` — clean, no conflicts, 13/13 files present.
2. **Recalibrate the spec, not the component.** Root cause (verified first-hand): W3 (diff #113, commit `bafb61d`) unconditionally added the 9th tab "Learning" (`gui/src/contrib.ts:83`), growing the tab strip to ~616px. The spec was calibrated in the 6-tab era (~457px; its own header said "Six tabs"). At the Playwright default 1280px viewport the panel clamps to min(720, 1280−220−400)=640px; clientWidth 640−61(chrome)=579 < 616 → the strip legitimately overflows → the ‹ › controls correctly stay visible → `toHaveCount(0)` fails. The test was already red at #113's verify log [31/150] (a full-gui diff never ran the gate between #113 and #123).
3. **Shared-helper drag-race fix (out of original scope, but required).** Gate 5 reruns with retries=0 exposed a pre-existing engagement race in `dragGripBy` shared by both tests' first drag — flaky on main, masked by CI retries (failure moved between the two tests across runs). Trace-decoded mechanism: the aside opens with `animate-[panel-in_0.2s]`, whose keyframes slide the panel from `translateX(10px)`; a grip `boundingBox()` sampled before/during the animation is stale by `mousedown`, and the grip's hit strip is only 8px wide (right-only) — the pointerdown landed ~4px off, the drag never engaged, width stayed 420px.
   - First fix attempt (x-stability probe over two consecutive rAF frames) failed: the trace showed the probe resolving at the animation's `from` position (x≈1031 = final 1020 + 10px) because the compositor hadn't advanced yet — both frames saw the pre-start position, a false "settled"; `mouseDown` at x=1033 then missed the strip ending ≤1029.
   - Final fix: in `openPanel`, drain `el.getAnimations().map(a => a.finished)` — animation objects exist the instant style applies, so this waits out the entrance deterministically — then plain `boundingBox()`. No animation-name coupling; probe deleted.
4. **GUI deps in the run worktree**: symlinked the main checkout's `node_modules` (epoch-40 convention); the worktree had none.

## Code changes

- **Archived W6 diff (13 files)**: `cmd_learning.go` (M), `cmd_learning_test.go` (A), `learning_actions.go` (A), `learning_actions_test.go` (A), `learning_replay/_stages/_status/protocol/server.go` (M), `LearningPanel.tsx` + `LearningPanel.test.tsx` (M), `mock-invoke.ts` (M), `types.ts` (M) — `promote --global` + drop/apply + stall wiring + tests.
- **`gui/e2e/context-panel-tabs.spec.ts` (+21/−7)**:
  - Second test ("controls disappear once every tab fits"): `setViewportSize 1440×900` scoped to this test only (MAX formula min(720, 1440−240−400)=720; strip 659px > 616px content), `dragGripBy(-520)` with poll expecting `"720px"`, header comment updated to the 9-tab reality (~616px vs ~359px strip at 420px; MIN clip ~397px).
  - First test (280px MIN): untouched except the shared-helper fix; drag math is relative to the current grip position and still polls `"280px"`.
  - `openPanel` helper: animation-drain before sampling the grip box.

## Verification — all 7 gates green

| # | Gate | Result |
|---|---|---|
| 1 | `go build ./... && go vet ./internal/...` | exit 0 |
| 2 | `go test ./internal/ipc/ -run Learning` | ok, 12.597s |
| 3 | `npx tsc --noEmit` | clean |
| 4 | vitest LearningPanel + contrib | 15/15 pass |
| 5 | playwright context-panel-tabs, 8 consecutive rounds, retries=0 | 16/16 pass (pre-fix failure rate ~25–50%) |
| 6 | full e2e (`/tmp/w6v2-e2e.log`) | 150 passed, 0 failed, exit 0 |
| 7 | `go test ./... -timeout=20m` (`/tmp/w6v2-go.log`) | 7 packages ok, 0 FAIL (ipc 577.7s, within baseline family), exit 0 |

Constraints held: worktree left dirty, zero commits, component logic untouched.

## Risk note

`openPanel`'s animation-drain now applies to every spec using the helper (currently only this file). If the panel ever gains a persistent/looping CSS animation, `a.finished` never resolves and stalls all cases in that file — current keyframes are one-shot entrance animations, so no present risk.

## Open loops

- All W6 v2 changes (archived diff + spec recalibration + helper race fix) sit uncommitted in the run worktree per the task's no-commit constraint — commit/land decision awaits the user, especially given the prior auto-land was blocked (`verify_failed`) and rejected.
- If the panel later gains persistent/looping CSS animations, revisit the `openPanel` animation-drain (`a.finished` would never resolve and hang the spec file).