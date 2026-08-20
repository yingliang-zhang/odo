> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# odo UI: Message Stream Rework on `ui/message-stream`

## Goal
Create a dedicated branch/worktree for odo UI development, starting with the center conversation message stream, using Hermes desktop as the design reference.

## Key decisions
- **Branch/worktree**: `ui/message-stream` @ `da9923a`, at `~/Projects/odo-worktrees/ui-message-stream`.
- **Worktree location outside `.odo/worktrees/`** — the daemon sweeper reclaims unbound directories (sweeper.go I8).
- **Clean start from main HEAD** — the prior `gui-wave` worktree retained unrelated loop changes.
- **Scope limited to the message stream** only; chrome rows (sidebar/header) untouched.
- **Reading width cap**: enabled previously unused `--chat-column-width: 1100px`.
- **Changes left uncommitted** — per Wave A/B convention, commit decision is the user's.

## Design mapping (odo before → Hermes pattern → change)
| odo before | Hermes pattern | Change |
|---|---|---|
| agent text flat, transparent, full-width, ~invisible 2% border | agent bubble with surface + hairline + 4px speaker corner | agent bubble gets `bg-raised` + stroke-secondary + radius 12/12/12/4, mirrored against user bubble |
| bubble-level copy only for code/tool-result | hover copy top-right with `Copied` feedback | hover copy (raw text) on user/agent bubbles |
| no timestamps | hover time + absolute time in title | 26px reserved bottom strip; hover shows time (user right / agent left), no layout shift |
| single tool call folded into "1 tool call" | single call shown inline; multi folded with last tool + args | lone call rendered inline; group summary `N tool calls · name(args…)`; spinner on trailing active group |
| preview dim italic, visually disconnected | streaming = same agent bubble shape + caret | preview reuses agent bubble base + caret, italic removed |
| no reading column (var defined, unused) | centered column with max-width | 1100px cap; run-group/bubbles/preview centered; chrome rows stay full-width |
| empty state = full-width button list | quiet suggestion chips | example prompts → wrapping chips |
| run-group-level `content-visibility` | also row-level | kept at group level |

## Code changes (5 files, uncommitted)
- `gui/src/components/MessageBubble.tsx` — agent bubble surface/border/radius; hover copy + timestamp strip; preview restyled on bubble base with caret.
- `gui/src/components/ChatSurface.tsx` — tool-call grouping: single call inline, burst fold with named summary, active-group spinner; 1100px centered column; empty-state chips.
- `gui/src/app.css` — styles for all of the above; `--chat-column-width: 1100px`.
- `gui/src/.../fixtures.ts` — conv1 fixture extended with a 2-call burst.
- `gui/tests/chat.spec.ts` — new contract test: "lone call inline; burst folds with named summary".

## Verification
- `tsc --noEmit` clean.
- Playwright e2e **91/91** (90 existing + 1 new).
- Browser visual check: surfaces, hover copy/timestamps, 1100px centered column confirmed.

## Incidents during work
- Apparent "3 tool groups" e2e failure traced to a reused dev server serving stale modules; actual burst rendering was correct (`2 tool calls · read_file(path: gui/src/App.tsx)`); verified via fresh local dev server DOM.
- Relative-path `_edit` twice landed in the old same-named worktree; both reverted cleanly (old worktree `git status` empty); subsequent edits used absolute paths.

## Open loops
- Commit/split decision for the 5 changed files — intentionally left to the user.
- Broader odo UI areas beyond the message stream (user framed this as the first step) — next priorities not yet chosen.