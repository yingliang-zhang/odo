# U1 Re-implementation After Diff #97 Auto-Rejection

## Context

Diff #97 (U1 UI layout lock, `docs/design/ui-layout-lock.md` §U1.1–U1.5) was auto-rejected by the review panel on a split verdict. Both dissenting legs converged on one root cause: the mandatory `npx vitest run` gate was never executed. The rejected diff never landed — the work tree was clean, so U1 was re-implemented from scratch.

## Panel dissent items and resolution

| # | Dissent | Resolution |
|---|---|---|
| 1 | `npx vitest run` never executed; new 324-line `statusbar.test.tsx` had no recorded run | Gate actually run this time. It surfaced real failures (`import.meta.url` non-file scheme under rolldown-vite, jsdom selector-text normalization, tsc `lastSeq` typing) which were fixed; final result 195/195 |
| 2 | `.queue-next-tag` never migrated but test asserted `font-size: var(--text-nano)` (`--text-nano` first defined in the diff) | Migrated `app.css`: `.queue-next-tag` `9px → var(--text-nano)`; CSSOM test assertions pass |
| 3 | `hiddenChipKeys()` classified hidden via `className.includes("hidden")` — substring also matches `overflow-hidden` | Hidden state now a dedicated `chip-hidden` token class (plus `hidden` utility); tests use `classList.contains("chip-hidden")` token-exact checks; `data-chip` attributes address elements |
| 4 | Unexplained `setTimeout → window.setTimeout` in SettingsPanel.tsx | **Kept, with justification**: HEAD contains commit `3cc0ae0` adding `@types/node`; bare `setTimeout` resolves to `NodeJS.Timeout`, conflicting with `useRef<number\|null>` — tsc was red here with clean tree because of this. Full sweep of `setTimeout/setInterval` in `gui/src` confirmed this is the only mismatch (FileRefContextMenu uses `ReturnType<typeof setTimeout>`; other bare calls discard the return value) |

## Delivered U1 scope (no U2 items)

- **U1.1 priority chip folding**: exported pure function `computeHiddenChipKeys` (test seam), footer width measurement on a width-stable container, ResizeObserver (50 ms debounce) plus post-render recheck, `+N` Radix Popover with live values (pipeline/OMP via `onSummaryChange` from always-mounted chips). Chips are always `display:none`, never unmounted — `.status-badge` e2e hooks stay alive, pinned by an e2e assertion.
- **U1.2 text tokens**: `STATUS_BADGE` 10px → `text-[length:var(--text-micro)]`; grievance/`.queue-next-tag` → new `--text-nano: 10px`; `.mono`/`.topbar-action` → caption; `.settings-title` → heading; `.diff-toggle` → label; `--text-nano` registered in tailwind.css.
- **U1.3 truncation**: global `.truncate` (160px cap) deleted; slash/mention rows get explicit `max-w-[220px]`.
- **U1.4 focus**: outline kill narrowed to `.chat-input textarea:focus`; focus-within border → `--accent-user` 55% color-mix; ModelPill trigger adds `focus-visible:ring-2` per CVA convention.
- **U1.5 layer**: `.toast-viewport` z-index 90 → 95.

## Changed files (scope: `gui/src/**` + `gui/e2e/**` only; lockfile untouched, verified after `npm ci`)

- `gui/src/components/StatusBar.tsx` — fold engine + `+N` popover + token updates (+496 lines)
- `gui/src/styles/app.css`, `gui/src/styles/tailwind.css` — tokens, `.truncate` removal, focus narrowing, z-95
- `gui/src/components/ChatSurface.tsx` — 220px row caps ×2, 55% accent border
- `gui/src/components/ModelPill.tsx` — focus-visible ring
- `gui/src/components/SettingsPanel.tsx` — `window.setTimeout` (dissent #4)
- `gui/src/strings.ts` — `overflowLabel`/`overflowTitle`
- `gui/src/components/statusbar.test.tsx` (new) — 20 tests: 7 pure-function, 4 component, 5 CSSOM, 3 structural, 1 ModelPill
- `gui/e2e/statusbar-overflow.spec.ts` (new) — 2 tests: fold + mount invariant + live popover rows; reflow recovery

## Verification — all three gates, in order

1. `./node_modules/.bin/tsc --noEmit` → OK (also proves SettingsPanel typing fix)
2. `./node_modules/.bin/vitest run` → 195/195 passed, 14 files
3. Targeted Playwright (`statusbar-overflow`, `wave-b`, `background-runs`, `pipeline-chip`, `sidebar`, `panel`, `context-panel-tabs`) → 39 passed (2.4m), no regressions

Changes left uncommitted in the work tree since 2026-08-28T23:09:00+08:00, awaiting the auto-land pipeline.

## Open loops

- Auto-land panel verdict on the re-submitted U1 diff — pending.