# Odo GUI Performance — Phase 1: Transcript Windowing, rAF Resize, Sidebar Animation

## Scope

Phase 1 of 4 from the read-only GUI perf review: transcript windowing, rAF-throttled panel resize, sidebar non-geometry collapse animation. Explicitly excluded (later phases): durable pages/right-pane restructure, design-contract sweep, chat-column width/bubble restyle, act() warning cleanup, bundle-size work. Original base `7e1bed8`; revision round re-applied to clean worktree at `c9afdda`. Baseline comparator: Hermes Desktop windows long transcripts; Odo re-rendered the full event history on every poll batch, cost climbing with session length.

## Key decisions

- **Windowing mechanism: run-group count tail window, `TRANSCRIPT_WINDOW_GROUPS = 25`** (named export from `gui/src/components/ChatSurface.tsx`). Chosen over a byte cap: deterministic counts in fixtures, one `slice` per render.
- **Render-only cut, zero information loss.** `fold`/`runRenderItems`/`runGroups` and all derive folds (`deriveLoopStates`, `derivePipelineStates`, `deriveTodoState`, `deriveParkedGoals`) keep reading the full events array in memory; ⌘F search matches over full `visibleEvents`, never the window (asserted by code comment and test).
- **Stepwise expansion only.** Top chip ("N earlier run groups hidden · click to expand", reusing the distill fold-chip idiom) reveals the previous 25 groups per click — never everything at once. Live run + preview bubble can never be windowed out.
- **Expansion state keyed per conversation** (`{key, pages}`), pages counting back from the live tail so streaming never re-keys; `Math.max(0,…)` absorbs array shrink; workstream switches can't leak expansion, and switch-back remembers it (ChatSurface stays mounted, swaps `conversationId` prop).
- **Search force-reveal via seq-membership scan** (`events.some(e => e.seq === seq)`): `runGroups` provably partitions all of `visibleEvents` (including the `start:null` preamble group), so equality lookup is immune to the journal's non-seq-ascending order at distill boundaries. Replaced the original seq-range test that missed matches outside run groups.
- **Sidebar animation: two distinct keyframes names** (collapse/expand). A same-name animation does not restart on an attribute-value flip (CSS mechanics); comment bars merging the duplicated keyframes.
- **Probe scaffolding deleted after measurement**, not committed.

## Code changes (final diff: 5 files, +176/−12)

| File | Change |
|---|---|
| `gui/src/components/ChatSurface.tsx` | `TRANSCRIPT_WINDOW_GROUPS = 25` export; tail-window `renderedGroups`; fold chip with stepwise expand; per-conversation expansion state; ⌘F jump-to-match force-reveals the matched group + everything below it via membership scan |
| `gui/src/components/ContextPanel.tsx` | Drag buffers `lastX` in `dragRef`; one `setPanelWidth` per animation frame via rAF; pointerup cancels the pending frame and commits the final width synchronously; unmount cancels. Verified it's the only per-move width drag (other `clientX` uses = context menus / scrollbar-thumb detection, no state commits) |
| `gui/src/components/Sidebar.tsx` | `data-sidebar-anim` set in `useLayoutEffect` (attr present at first paint, no flash), cleared on the aside's own `animationend` (target-guarded against child fades); mid-animation re-toggle restarts via name flip |
| `gui/src/styles/app.css` | Removed the 240→48 width/padding transition (geometry flips instantly — one relayout per toggle); motion is `transform:translateX` slide + label fade keyframes gated on `[data-sidebar-anim]` |
| `gui/e2e/transcript-windowing.spec.ts` (new) | `transcript windowing: tail window, stepwise expand, full-array search` |

## Review round (auto_revise) — findings closed

1. **e2e seeding not via fixtures seam** (dead `__odoFixtures` declaration + 54 composer sends + 240s timeout) → rewritten as one `page.evaluate` using `fx.ev`/`fx.events.push` to inject 29 run groups: **2.7s** vs 48.6s composer-seeded.
2. **`baseGroups < N` guard fragile to fixture drift** (second-click math uncovered) → all assertions derived from the boot DOM count; needle sits EXTRA=4 groups above the cut by construction (hidden at 0 clicks, revealed at 1, valid for any base); stepwise per `min(total, N·(1+k))`; singular/plural chip text derived by count.
3. **Force-reveal missed matches outside run-group seq ranges** → membership lookup (above).
4. **Keying comment overclaimed** "(conversation, current window start)" → comment corrected to actual semantics.
5. **Second per-move resize source suspected** → confirmed none exists.
6. **First-frame flash + no restart on mid-animation re-toggle** → `useLayoutEffect` + dual keyframes.

## Verification

- `tsc --noEmit` green; vitest **166/166**; Playwright **120/120 (4.9m)** including the new spec.
- Perf probe (then deleted): 372 events → 25 groups / ~50 bubbles rendered vs unbounded; sidebar collapse distinct chat-column widths **9 → 1**; rAF resize ≤1 width commit per frame, final commit synchronous; existing drag assertions race-free (all run post-`mouse.up`).
- Live DOM probe of anim lifecycle: no attr on mount → `collapse`/`expand` values with matching computed `animation-name` → attr cleared after each duration; rapid re-toggle flips animation name (restart path works).
- Environment notes: worktree lacked `gui/node_modules` (npm ci required); tool path-mapping intermittently resolved `gui/e2e` against the main checkout — shell was the ground truth for file state.

## Open loops

- **Review follow-up items 2–4 of 4** remain (this was order item 1): durable pages/Knowledge right-pane restructure, single design-contract sweep, chat-column width + bubble restyle, act() warning cleanup, bundle-size work — Phase 2+ scope, not started.
- **rAF resize claim is structural, not a measured speedup**: headless synthetic drags run frames uncapped, so per-frame cadence parity (≤1 commit/frame) and no-regression commit counts are proven, but real-device pointer-cadence numbers were not captured — a headed/manual DevTools profile would close this.
- Phase 1 changes live in the worktree diff (base `c9afdda`); landing/merge status not recorded in this conversation.