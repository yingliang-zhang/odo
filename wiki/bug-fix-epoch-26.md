# Adoption Lock P1 Implementation (odo GUI)

Session implemented P1.1–P1.5 of `docs/design/adoption-lock.md` in the odo repo (worktree at `dd74425`), scoped strictly to `gui/src/**` and `gui/e2e/**`. P2/P3 explicitly untouched (no preview changes, Runs tab, perf harness, theme cascade, esc registry).

## Key decisions

- **P1.1 Journal search**: ⌘K palette gained a typed-only dynamic group; query ≥2 chars triggers 250ms-debounced `search_events` fan-out across every registered project (read-only). Rows render `project · workstream · type · snippet`. Enter performs a one-flight foreign workstream switch by reusing the existing `handleSwitchWorkstream(wsId, root)` path (Sidebar.tsx:374-382) rather than inventing switch semantics, then opens ⌘F prefilled with the query. The full stack (daemon SearchEvents, Tauri command, wire types, fixtures) already existed — only the GUI consumer was missing.
- **P1.2 Selector map**: new `gui/src/slots.ts` with typed `SLOT` constant map + `slotSel()`; `data-slot` attributes added alongside existing markers (composer, statusbar chip row, panel tabs, diff card, palette/shortcuts dialogs). Zero existing selectors rewritten.
- **P1.3 Keybind registry**: new `gui/src/keybinds.ts` — typed static table `{id, label, combo, display, category, allowedInInput}`; App's mod-combo dispatch now table-driven via `matchKeyEvent`. ⌘/ opens a read-only ShortcutsPanel (Radix Dialog, Esc stopPropagation). Palette hints render live `comboFor(actionId)`, replacing six hardcoded strings. `label` added beyond the spec shape (panel rows need text); Esc rows are display-only — the Esc ladder stays imperative by design.
- **P1.4 Tool-result inline diffs**: `looksLikeUnifiedDiff` gate (git preamble or bare `---/+++` pair); MessageBubble swaps `<pre>` for compact read-only `ToolDiffView`, reusing DiffViewer's `parseFileSegments` (exported — no second parser). Run-group header gained an `{N} files changed` chip derived from journaled results; click opens the Changes tab preselected. Zero new IPC; no accept/reject actions.
- **P1.5 Error summarizer**: new `gui/src/errors.ts` — 10 ordered `[pattern → {summary, action?}]` rules grounded in the Rust bridge's `format!` strings (verified in lib.rs). Classified errors render summary + action with raw text in `title`, `data-sticky`, ×-dismiss only (no auto-10s timeout); unclassified errors keep legacy raw + auto-dismiss; toasts unchanged. Rule order matters: restart-failed precedes generic connect.
- **Sticky banner interplay**: poll-recovery self-heal still clears a sticky `poll failed:` banner on the next healthy tick — intentional (recovery beats stickiness).

## Verification (all gates green)

1. `npx tsc --noEmit` → clean.
2. `npx vitest run` → 24 test files, 262 tests passed (221 baseline + 41 new).
3. Targeted Playwright (`journal-search.spec.ts`, `shortcuts.spec.ts`, `chat.spec.ts`) → 16 passed. Full suite regression check: 137 passed.

## Changed files

- **New**: `slots.ts`, `keybinds.ts`, `errors.ts`, `journal_search.ts`, `components/ShortcutsPanel.tsx`; tests `slots.test.tsx`, `keybinds.test.tsx`, `errors.test.ts`, `journal_search.test.ts`, `app_journal_search.test.tsx`, `commandpalette.test.tsx`, `tooldiff.test.tsx`; `e2e/journal-search.spec.ts`.
- **Modified**: `App.tsx`, `api.ts`, `styles/app.css`, `CommandPalette.tsx`, `ChatSurface.tsx`, `MessageBubble.tsx`, `DiffViewer.tsx`, `ContextPanel.tsx`, `StatusBar.tsx`, `dev/fixtures.ts`, `dev/mock-invoke.ts`, `e2e/shortcuts.spec.ts`, `e2e/chat.spec.ts`.

Fixture notes: `makeSearchResults` upgraded to be daemon-faithful (project-scoped, conversation→workstream join, newest-first). Two test-only bugs fixed en route (duplicate "Command palette" aria-labels → placeholder queries; Radix portal scoping — palette tests must query `document`, not `container`). One real production bug surfaced by tests: ToolDiffView hunk rows missing a marker class — fixed in the component.

Worktree left uncommitted for the auto-land pipeline; `git status` verified no `package.json`/lockfile/`.odo-verify`/Go changes.

## Open loops

- `JOURNAL_HIT_CAP = 8` caps palette journal results; the daemon's 100/project limit remains reachable only via `odo journal search` CLI —未 decided whether the GUI should expose deeper results.
- P2/P3 items from the adoption-lock spec remain unimplemented by design (preview changes, Runs tab, perf harness, theme cascade, esc registry) — awaiting their own tasking.