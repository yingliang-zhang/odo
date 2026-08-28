# odo GUI — UI Layout Lock: U1 Re-land and U2 Implementation

## Context

- Diff #98 (U1: statusbar overflow fold) was auto-rejected twice (#97/#98) because the gui-scoped verify command in `.odo-verify` omitted vitest, so no vitest run ever appeared in the mechanical verify record.
- Fix landed at commit `20f08e4`: gui verify is now `npx tsc --noEmit && npx vitest run && npx playwright test --reporter=line`. Local baseline: vitest 175/175 PASS.
- Mechanical verify now runs all three gates before the panel sees any gui diff.

## U1 re-apply (third attempt — accepted by auto-panel)

Re-applied diff #98 (`.odo/diffs/6a91a116-…diff`, 9 files, gui-only) onto HEAD `20f08e4` via clean `git apply --check`.

- **Dissent #2 resolved**: `.queue-next-tag` migrated to `font-size: var(--text-nano)` in `app.css:891-896`; `--text-nano: 10px` registered in `:root` and `tailwind.css @theme`.
- **Dissent #3 resolved**: zero `.includes("hidden")` substring checks; hidden state uses dedicated `chip-hidden` token + `classList.contains("chip-hidden")` + `data-chip` attribute addressing.
- **`window.setTimeout` in SettingsPanel kept and proven required**: reverting to bare `setTimeout` reproduces `TS2322: Type 'Timeout' is not assignable to type 'number'` (@types/node `3cc0ae0`).
- Changed files (9): `StatusBar.tsx` (+496, fold engine + `+N` popover), `ChatSurface.tsx`, `ModelPill.tsx`, `SettingsPanel.tsx`, `strings.ts`, `app.css`, `tailwind.css`, `statusbar.test.tsx` (new, 20 tests), `e2e/statusbar-overflow.spec.ts` (new).
- Gates: tsc exit 0; vitest **195/195** (175 baseline + 20 new); targeted Playwright **39 passed**.
- U1 subsequently landed as commit `cf66635`.

## U2 implementation (U2.1–U2.5 of docs/design/ui-layout-lock.md)

### Code changes (11 files, gui/src/** + gui/e2e/** only)

| File | Change |
|---|---|
| `gui/src/panel_overlay.ts` (new) | Constants (280/720/420/560/600), hysteresis state machine `nextPanelOverlay`, drag clamp, storage read, `usePanelOverlay` hook |
| `gui/src/components/ContextPanel.tsx` | Overlay prop + `.panel-scrim`, width persistence, dynamic drag max; `max-[999px]:fixed` breakpoint deleted (one mechanism only) |
| `gui/src/App.tsx` | `.app-main` callback ref + hook wiring; scrim/panel close via existing ⌘J/Esc paths |
| `gui/src/styles/app.css` | `--panel-width: 420px` default (was 380), U2.4 72ch prose rule, stale 999px comments removed |
| `gui/src/components/MessageBubble.tsx` | `pb-[26px]` → `pb-[20px]` on both bubble variants |
| Tests (new) | `panel_overlay.test.ts` (13), `contextpanel.test.tsx` (11), `messagebubble.test.tsx` (2), `e2e/panel-overlay.spec.ts` (3) |
| Tests (updated) | `context-panel-tabs.spec.ts` (drag start 120→160, MAX 600→640), `statusbar.test.tsx` (stale "≤999px" test name) |

### Key design decisions and root-caused fixes

1. **Self-oscillation guard (U2.1)**: overlay makes the panel `fixed`, removing it from the body grid, so chat immediately widens ~420px and a naive reading crosses 600 → flips back to docked forever. Hysteresis evaluates **docked-equivalent width** (`chatWidth − panel.offsetWidth` while overlayed).
2. **`booted` early-return swallowed the ResizeObserver effect** (real bug found by e2e): App.tsx renders only a loading div until the daemon connects; `<main>` never mounts, so a `useRef` + one-shot effect reads null and never resubscribes. Fixed with a callback ref (element stored via `useState`) and the effect depending on the element.
3. **`clientWidth` off-by-one**: Chromium border-box gives sidebar `clientWidth=239` (excludes 1px border) → drag max 641≠640. Switched to `offsetWidth`, matching the `window − sidebar − 400` formula semantics.
4. **Scrim implemented literally as click-through** (`pointer-events-none`, chat stays interactive); close paths are the existing ⌘J/TopBar/Esc. Spec wording was ambiguous — see Open loops.
5. **`Number("") === 0` storage bug**: empty `odo-panel-width` value would clamp to 280; `readStoredPanelWidth` now falls back to default on empty/unparseable values.

### Gates (all run in-worktree, verbatim in report)

- Gate 1 `tsc --noEmit` → exit 0.
- Gate 2 `vitest run` → **221/221 PASS** (17 files; 195 baseline + 26 new).
- Gate 3 targeted Playwright (`panel-overlay`, `panel`, `sidebar`, `statusbar-overflow`, `context-panel-tabs`) → **20 passed (47.4s)**.
- Failures along the way were root-caused, not suppressed: 3 unit tests (empty-string parse, ch quantization, comment containing literal selector) + 4 e2e (unsubscribed effect, 641px off-by-one, Chromium serializing scrim color as `oklab` — assertion changed to class + computed pointer-events).
- Constraints held: no package.json/lockfile/`.odo-verify`/Go changes (`git status` verified); `npm ci` used only to install gate dependencies; temporary probe spec and dev server cleaned up. Worktree left uncommitted for the auto-land pipeline.

## Open loops

- Scrim semantics: implemented as click-through per literal spec wording; if the intended behavior is click-to-dismiss, it is a one-line change awaiting user/panel confirmation.
- U2 diff's auto-land panel verdict not yet observed in this session (worktree left uncommitted for the pipeline).