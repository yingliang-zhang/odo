# DESIGN LOCK: Auto-Land Pipeline Status Indicator (Phase 2 shipped)

> Tri-model MoA (K3/GLM/DSF, --thinking max, 540s, blind-sealed). 3/3 converged on GUI-only, no new IPC.
> Phase 2 amendment (2026-08-19): daemon `auto_land_started` stage breadcrumbs close the silent window rule 10 deferred.

## Core insight (3/3)

All terminal pipeline events are already journaled as `review_action{actor:"auto_panel"}` and flow to the GUI via bootstrap replay + poll. No new IPC needed. The one honest gap: between pipeline start and the first journaled row (verify + panel are silent — up to 10+ min). Phase 1 labeled this "in flight" instead of lying — but only after refresh rows; with a fresh base the chip sat at **queued** through the whole window.

**Phase 2:** the daemon journals `auto_land_started{stage:"verify"|"panel", patch_sha16}` immediately before each silent stage (after every free gate, before `runVerifyGate` / `reviewFanout`). The chip now reads the stage verbatim — "verify running…", "panel reviewing…" — and "queued" only remains for the genuinely pre-pipeline instant (pref on, pending, pipeline not yet triggered). Breadcrumbs carry NO risk receipt (liveness, not an outcome — never rated) and never fold into distill prompts (`foldExcludedReviewAction`), render no transcript bubble (chip is their surface; LedgerPanel keeps the verbatim rows), and gates that block before any spend leave zero breadcrumbs (queued → blocked directly).

## States (per pending diff_id, latest auto_panel event wins)

| State | Derivation | Display |
|---|---|---|
| **hidden** | pref off OR no pending diffs with auto_panel activity | chip absent |
| **queued** | diff pending, auto_apply=="main", no auto_panel event yet (pre-pipeline instant only) | spinner "auto-land queued…" |
| **in flight** | auto_land_started{stage} or refresh_attempted{clean} newest, no terminal event | spinner stage-verbatim: "verify running…" / "panel reviewing…" (fallback "verify → panel…"; refresh_attempted→"refreshed", auto_revise_round→"repair round N") |
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
| `gui/src/pipeline.ts` (new) | `PipelinePhase` type, `PipelineState` interface, `derivePipelineStates(events, pendingDiffIds, autoApplyOn)` function; Phase 2: `stage` field + `auto_land_started` case |
| `gui/src/components/StatusBar.tsx` | Add `PipelineChip` component between PanelChip and diff-count badge; Phase 2: stage-verbatim in-flight labels |
| `gui/src/App.tsx` | Wire `derivePipelineStates` in event handler, pass to StatusBar |
| `gui/src/styles/app.css` | `.auto-land-chip` + popover row variants (reuse existing tokens) |
| `gui/src/components/MessageBubble.tsx` | Phase 2: `auto_land_started` renders nothing (todo_merge posture) |
| `gui/src/dev/fixtures.ts` | Add pipeline event fixtures for dev mode |
| `gui/e2e/pipeline-chip.spec.ts` (new) | E2E tests; Phase 2: stage breadcrumb labels + no transcript bubbles |
| `internal/ipc/autoland.go` | Phase 2: `journalAutoLandStarted` before `runVerifyGate` and `reviewFanout` |
| `internal/ipc/server.go` | Phase 2: `foldExcludedReviewAction` excludes `auto_land_started` from distill prompts |

**Phase 1: no daemon changes, no new IPC, no `internal/` files touched. Phase 2: daemon journals stage breadcrumbs — same `review_action` event type, same actor filter, still no new IPC.**

## Hard rules

1. Journal-derived only — no new IPC, no new event types
2. Per-conversation scope (not cross-workstream)
3. Actor filter `auto_panel` only — human actions don't trigger the chip
4. Reasons verbatim (forward-compat: unknown values render plainly)
5. Zero GUI-side state latches — everything re-derives from events + diffs
6. Feature hidden when `auto_apply !== "main"`
7. Transient success ≤4s, blocked sticky while diff pending
8. Distill/curate/learner/memory machinery out of scope
9. LedgerPanel remains the full-history surface — chip is current-only; breadcrumbs render no transcript bubble
10. ~~"In flight" is honest — don't pretend to distinguish verify vs panel without daemon events~~ **(Phase 2, done):** daemon `auto_land_started` rows name the stage; the chip renders the journaled stage verbatim, never an invented one

## Verification

```bash
go test ./internal/ipc/ -run 'AutoLand|Settle|DistillRender'
cd gui && npx tsc --noEmit
cd gui && npx playwright test pipeline-chip.spec.ts
cd gui && npx playwright test --grep "StatusBar|sidebar|background-runs|wave-b"
```
