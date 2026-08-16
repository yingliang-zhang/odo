# DESIGN LOCK: Auto-Land Pipeline Status Indicator (GUI-only Phase 1)

> Tri-model MoA (K3/GLM/DSF, --thinking max, 540s, blind-sealed). 3/3 converged on GUI-only, no new IPC.

## Core insight (3/3)

All terminal pipeline events are already journaled as `review_action{actor:"auto_panel"}` and flow to the GUI via bootstrap replay + poll. No new IPC needed. The one honest gap: between pipeline start and the first journaled row (verify + panel are silent — up to 10+ min). The indicator labels this "in flight" instead of lying.

## States (per pending diff_id, latest auto_panel event wins)

| State | Derivation | Display |
|---|---|---|
| **hidden** | pref off OR no pending diffs with auto_panel activity | chip absent |
| **queued** | diff pending, auto_apply=="main", no auto_panel event yet | spinner "auto-land queued…" |
| **in flight** | refresh_attempted{clean} or no refresh needed, no terminal event | spinner "verify → panel…" (refined by latest non-terminal: refresh_attempted→"refreshed", auto_revise_round→"repair round N") |
| **landing** | moa_review{accept} is newest, no accept after | spinner "landing…" |
| **landed** | accept{actor:"auto_panel"} | green flash ≤4s transient |
| **blocked** | auto_land_blocked{reason} | sticky, reason shown verbatim |
| **suspended** | latest memory_update{auto_land} is ladder_suspended | sticky "suspended" |
| **revise** | auto_revise_round{round:N} newest, no terminal | spinner "repair round N" |

Human accept/reject = silent (not pipeline news).

## Placement

StatusBar `PipelineChip`, between `PanelChip` and diff-count badge. Reuse `PanelChip` pattern: `useCloseOnClickAway`, `status-badge` classes, `bg-runs-menu`-shaped popover. Popover: one row per tracked diff (state icon · diff # · label · reason/round). Row click → Review tab.

## Files

| File | Change |
|---|---|
| `gui/src/pipeline.ts` (new) | `PipelinePhase` type, `PipelineState` interface, `derivePipelineStates(events, pendingDiffIds, autoApplyOn)` function |
| `gui/src/components/StatusBar.tsx` | Add `PipelineChip` component between PanelChip and diff-count badge |
| `gui/src/App.tsx` | Wire `derivePipelineStates` in event handler, pass to StatusBar |
| `gui/src/styles/app.css` | `.auto-land-chip` + popover row variants (reuse existing tokens) |
| `gui/src/dev/fixtures.ts` | Add pipeline event fixtures for dev mode |
| `gui/e2e/pipeline-chip.spec.ts` (new) | E2E tests |

**No daemon changes. No new IPC. No `internal/` files touched.**

## Hard rules

1. Journal-derived only — no new IPC, no new event types
2. Per-conversation scope (not cross-workstream)
3. Actor filter `auto_panel` only — human actions don't trigger the chip
4. Reasons verbatim (forward-compat: unknown values render plainly)
5. Zero GUI-side state latches — everything re-derives from events + diffs
6. Feature hidden when `auto_apply !== "main"`
7. Transient success ≤4s, blocked sticky while diff pending
8. Distill/curate/learner/memory machinery out of scope
9. LedgerPanel remains the full-history surface — chip is current-only
10. "In flight" is honest — don't pretend to distinguish verify vs panel without daemon events (Phase 2 follow-up)

## Verification

```bash
cd gui && npx tsc --noEmit
cd gui && npx playwright test pipeline-chip.spec.ts
cd gui && npx playwright test --grep "StatusBar|sidebar|background-runs|wave-b"
```
