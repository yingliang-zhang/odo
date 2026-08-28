# U1 UI Layout Hardening Lock — Implementation (Retry After Supply-Chain Block)

## Context

Retry of U1 (`docs/design/ui-layout-lock.md` §U1) after diff #96 was blocked: the code was correct but the diff included `gui/package-lock.json` + `@types/node` dev dep, tripping the fail-closed supply-chain gate. The human committed the dep separately (commit `3cc0ae0`, `@types/node ^22.20.1`). This attempt MUST NOT touch `gui/package.json` or `gui/package-lock.json`; scope restricted to `gui/src/**` and `gui/e2e/**`; Go files, `gatepolicy.go`, `.odo-verify` forbidden; Go suite not run.

## Delivered scope (all 5 U1 items)

1. **U1.1** StatusBar hide-by-priority overflow + "+N" popover; chips stay mounted (visibility class only), ResizeObserver + debounce, tabsOverflow pattern reused.
2. **U1.2** Type-token sweep: STATUS_BADGE 10px → `text-micro`; 9px badges → new `--text-nano: 10px`; hardcoded px mapped to tokens.
3. **U1.3** Global `.truncate` cap deleted (app.css); explicit `max-w-[220px]` on slash-menu/mention-menu rows.
4. **U1.4** Composer focus: outline kill scoped to `.chat-input textarea:focus` only; focus-within border → `color-mix(in srgb, var(--accent-user) 55%, transparent)`; ModelPill trigger `focus-visible:ring-2` (via CVA).
5. **U1.5** `.toast-viewport` z-index 90 → 95.

## Changed files

| File | Change |
|---|---|
| `gui/src/components/StatusBar.tsx` | U1.1 overflow mechanism + U1.2 token sweep |
| `gui/src/styles/app.css` | `--text-nano` + token mapping; `.truncate` deleted; outline kill narrowed; z-95 |
| `gui/src/styles/tailwind.css` | `@theme inline` registers `--text-nano` |
| `gui/src/components/ChatSurface.tsx` | slash/mention `max-w-[220px]`; focus-within 55% accent border |
| `gui/src/components/ModelPill.tsx` | trigger CVA focus ring |
| `gui/src/components/SettingsPanel.tsx` | `setTimeout` → `window.setTimeout` (tsc fix) |
| `gui/src/strings.ts` | two statusbar-overflow string keys |
| `gui/src/components/statusbar.test.tsx` (new) | 13 U1 tests |

## Key decisions

- **Overflow priority** `OVERFLOW_RANK`: ctx-meter(0) → OMP(1) → Panel×N(2) → pipeline/running(3) → finished/bg-runs(4) → count chips(5); same-rank ties broken by DOM order. Pure `computeHiddenChipKeys` exported as test seam.
- **Mount invariant**: each chip wrapped in `[data-chip]` wrapper, `hidden` class toggled, never unmounted; e2e `.status-badge` hooks stay live after hiding.
- **Self-lock bug (root cause of first e2e run's 11 failures)**: chips row width was content-adaptive — hiding chips narrowed the row, shrinking `clientWidth`, which hid more chips with no rebound (tabsOverflow works because its container width is stable). Fix: row became `flex-1 justify-end overflow-hidden`, making `clientWidth` a stable available-width signal.
- **+N popover**: Radix Popover (same pattern as TopBar ⋯), one row per hidden chip with live values; actionable rows (pending diffs/wiki/memory/pipeline/bg-runs) click-to-navigate; OMP value flows via `onSummaryChange` so fetching continues while its chip is hidden. +N width starts at a 46px estimate, then measured and converged.
- **SettingsPanel one-liner**: after `@types/node` landed, bare `setTimeout` resolved to `NodeJS.Timeout` and `tsc` was red at HEAD; `window.setTimeout` (DOM number) is the minimal in-scope fix.
- **twMerge avoidance**: inside `cn()` merges use `text-[length:var(...)]` (existing `CTX_POP_TITLE` convention); a bare token would be dropped by the color-group merge.

## Verification (all gates green)

- Gate 1 `npx tsc --noEmit` → `TSC_OK`
- Gate 2 `./node_modules/.bin/vitest run` → 14 files, **188/188 passed**, 2.22s
- Gate 3 `./node_modules/.bin/playwright test e2e/wave-b.spec.ts e2e/background-runs.spec.ts e2e/pipeline-chip.spec.ts e2e/sidebar.spec.ts e2e/panel.spec.ts e2e/context-panel-tabs.spec.ts` → **37 passed (2.1m)**. First run had 11 statusbar-chip failures (the self-lock bug above); all green after the fix, then tsc + vitest re-run to confirm.

Test coverage in `statusbar.test.tsx` (13 cases): priority ordering, +N width accounting, all-hidden degradation, DOM tie-break, mount invariant + popover live values + row-click navigation, rebound, toast z-95, `.truncate` removal, textarea focus scoping, token rule declarations, composer 55% accent border, ModelPill ring, menu 220px cap.

## Known boundaries

- `app.css?raw` is swallowed to an empty string by the Tailwind vite plugin under vitest → tests read the source via `node:fs` and inject it into jsdom CSSOM (behavioral assertions, not source-text).
- jsdom cssstyle does not resolve `var()` → token mapping asserted on rule declarations (`getPropertyValue`); real pixel values are covered by e2e.
- `ContextPanel`'s `panel-tab-badge text-[10px]` deliberately not swept: U1.2 scope is StatusBar.tsx + app.css only; that file belongs to U2.

## Open loops

- U2 items in `docs/design/ui-layout-lock.md` remain unimplemented (out of scope for this task).
- `ContextPanel` `panel-tab-badge text-[10px]` token sweep deferred to U2 (file reserved for U2 changes).