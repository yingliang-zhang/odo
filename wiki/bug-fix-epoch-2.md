> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Workstream Switch Scroll Repin Fix (odo GUI)

## Problem
Switching workstreams did not scroll the view to the newest content at the bottom; a stale "new output" pill could also remain.

## Root Cause
`App.tsx`'s `applyBootstrap` wholesale-replaces `events` on workstream switch, but `ChatSurface` does not remount. The stick-to-bottom state `stickRef` leaked across sessions: if the previous session had been scrolled up (`stick=false`), the new session stayed off-bottom.

## Code Changes
| File | Change |
|---|---|
| `gui/src/components/ChatSurface.tsx:604-612` | New `conversationId`-change effect: reset `stickRef=true`, clear pill, `pinToBottom()`; subsequent `events.length` effect + ResizeObserver re-anchor |
| `gui/e2e/auto-follow.spec.ts` | New case "switching workstreams re-pins to the newest output"; `fillHistory` supports per-session/per-count history injection |

**Test design critical**: the two sessions must have unequal-length histories (12 vs 72 messages). With equal lengths, browser scroll anchoring coincidentally lands at the bottom and masks the bug — first verification round false-passed this way.

## Verification
- Without fix: new case fails (12533px from bottom); other 2 scroll cases unaffected
- With fix: auto-follow 3/3 + sidebar 7/7 pass; `tsc --noEmit` clean; vitest 57/57
- Control condition confirmed on same dev server: no-fix fails, fix passes

## Landing Status
- Fix staged in worktree `6a8582bc`; entered auto-land pipeline as diff #7, `base_sha` = main HEAD `85eaef8` (not stale)
- Refresh probe (`refresh_attempted`, seq 529): clean, no conflicts
- Status check methodology: traced diff via journal store + daemon.log; confirmed within legitimate silent window (~25min threshold = 10min verify timeout + 900s/leg panel), not stuck per `bug-fix-epoch-1` diagnostic anchors
- Final outcome: `review_action accept` by `auto_panel` (seq 600) — diff accepted

## Side Discovery (not fixed)
Port 1420 was occupied by another worktree's (`6a8582de`) vite dev server; `playwright.config`'s `reuseExistingServer: true` silently reused the wrong build during verification, producing a false pass. Setting it to `false` makes port conflicts hard-fail instead.

## Open loops
- `playwright.config` `reuseExistingServer: true → false`: recommended, but touches cross-worktree parallel workflow habits — left for user decision.