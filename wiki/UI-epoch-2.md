> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Odo GUI — Hermes Alignment Deploys and Streaming/Composer Fixes (2026-08-19)

## Decisions

- **Deployment pipeline** (repeated 3×): worktree fix → commit → rebase onto main HEAD → FF merge → `npm run tauri:build` in the main checkout (warm Rust target, ~30–50s) → replace `/Applications/Odo.app` via `ditto` → launch. Running instance only needs restart when source changed after install.
- **`/panel` has no consolidator** — confirmed at `internal/ipc/server.go:2530` `handlePanelQuery`: N parallel `QueryWithTools` legs concatenated verbatim by `formatPanelResults`. `internal/ipc/design_moa.go` `runDesignMoa` already contains the consolidator pattern (4th `moa.Query`, orchestrator model, labeled legs, wire receipts). The `server.go:2422` "no 4th model call" rule applies to review-verdict vote counting only, so panel Q&A synthesis does not violate it. Implementation drafted; gated on user choice (Open loops).
- **Auto-follow semantics**: un-stick only on explicit user gestures, never on scroll events (browser scroll anchoring and `content-visibility` size resolution move `scrollTop` passively and were misread as user intent).
- **Composer is uncontrolled** for React 19 composition semantics; programmatic writes gated on `!composing`.
- **WebKit-only bugs need WebKit harnesses**: Playwright default Chromium missed two bugs; verification added Playwright-WebKit plus a native WKWebView (swiftc) harness.

## Code changes (all FF-merged to main, built, installed to /Applications)

| Commit | Change |
|---|---|
| `67049fa` (was `440209f`) | Merge `ui/message-stream` worktree (5 files: main features + 8 Hermes alignment items) into main. Pre-check `git log ui/message-stream..main` showed main's post-`da9923a` commits touched only review_action filtering — no UI hunk overlap. tsc clean, Playwright 92/92. |
| `9022e22` | **Auto-follow scroll fix** (`gui/src/components/ChatSurface.tsx`). Root causes: (A) `handleListScroll` treated any scroll landing >80px from bottom as user-leave, so content-visibility resolution/scroll anchoring killed stick permanently (pill only, no follow); (B) mount pin computed against 200px `contain-intrinsic-size` estimate, conversation opened 810px from bottom. Fix: un-stick only via wheel-up (ToolTicker inner scroll excluded)/touch pull-down/scrollbar drag; programmatic writes get 250ms suppression; ResizeObserver on content wrapper + list re-pins invisible growth; search jump-to-match explicitly unfollows. New `e2e/auto-follow.spec.ts` (2 contracts). vitest 57/57, Playwright 94/94. |
| `67dac9c` | **Composer initial-height bug** (`ChatSurface.tsx`). Root cause: persisted panel-open (`odo-panel-open`) squeezes textarea to 98px at 1000px window → placeholder wraps; WebKit counts wrapped placeholder in `scrollHeight` (Chromium does not) → mount effect sized to 78px (101px with running placeholder), first keystroke re-measured to 39px. Fix: placeholder cleared during measurement (same-frame restore) + width-gated ResizeObserver re-size. Verified via counterfactual baseline, Playwright-WebKit matrix, native WKWebView harness; bundle grep confirmed `.placeholder=""`. |
| `ffd2b0d` | **Chat-column alignment + steering IME safety**. (1) `.tool-ticker`, preview bubbles, panel-thinking rows hung on the unconstrained container (only run-group had the 1100px column) — all three wrapped in `max-w-[var(--chat-column-width)] mx-auto` (1400px viewport: left edges now all 270). (2) Steering text destroyed during runs: React 19 ignores input events during composition → stale `draft` written back by 350ms poll / 1s heartbeat rerender, killing Chinese IME composition (ASCII unaffected). Fix: composer uncontrolled (`defaultValue`); programmatic writes (send-clear, slash pick, edit backfill) via layout effect gated on `!composing`; `compositionend` syncs committed text to `draft` via extracted `handleDraftChangeValue`. Fixtures gained `previewState` knob (isomorphic to `runState.foreground`) for M7 streaming-preview e2e. New contracts: `e2e/chat-column.spec.ts` (3), `e2e/steer-composition.spec.ts` (3). tsc clean, vitest 57/57, Playwright **100/100**. |

## Panel leg status (journal seq 298)

- t9s/kimi-k3 ✓ full; glm-5.2 ✓ full (DOM/CSS line-level diagnosis + merge-order check); t9s/deepseek-v4-flash ✗ failed (`tool loop exceeded 16 rounds`, zero information).

## Environment lessons

- Zombie vite on :1420 from `ui-message-stream` worktree made e2e silently run stale code (recurrence of ui-epoch-1 incident) — killed; check port ownership before running e2e.
- `bash` tool `cwd` parameter repeatedly failed to take effect → Playwright resolved config from wrong directory; stable workaround: `eval` + `Bun.spawn({cwd})`.

## Open loops

- `/panel` consolidator: **default-on** (reuse prefs `orchestrator:` line) vs **pref-gated** (`panel_consolidator:` line) — awaiting user's pick before implementing.
- `/` command prompt not appearing: no verdict (both code paths byte-identical; trigger regex `^\/(?:\S*\s?\S*)?$` requires `/` as first draft char) — retest in current build; only then investigate IME.
- 1–2px height jump on first keystroke (CSS `min-h-[36px]` vs `scrollHeight≈37px` mismatch, no `transition-[height]` — glm-leg hypothesis) — never measured; confirm whether still observable after `67dac9c`.
- Composer button row squeezes input to ~98px when panel open (functional, placeholder truncates) — optional wrap/width-budget improvement offered, not taken.
- `ui/message-stream` branch/worktree fully merged into main — deletion optional.