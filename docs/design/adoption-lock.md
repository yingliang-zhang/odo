# Adoption Lock — Odo ← Hermes Desktop (2026-08-29)

Four-leg blind comparison (kimi-k3 / glm-5.3-flash / deepseek-v4-flash / gpt-5.6-sol, sealed 900s, thinking max) against brief `/tmp/odo-hermes-compare-brief.md`. Consolidated rulings below are the binding spec for three implementation packages (P1 → P2 → P3), each through the Odo auto-land pipeline (gui verify: tsc + vitest + playwright; 3-leg panel; D7 settle table governs dissent).

## Global verdicts (locked)

- **NO pane-tree workspace** (4/4): Odo's single-conversation poll architecture, Esc ladder, and journal invariants reject the tree. Take only: keep-alive LRU park (P2) and a fixed panel split point (P3) if demand proves out.
- **NO CDP element automation / act / tour** (4/4): locked P0 boundary. Preview upgrades are capture/read + GUI-initiated sandboxed viewing only.
- **NO runtime plugin loader** (4/4): internal contribution registry only (P3); skills stay the extension surface.
- **Widget reply channel: deferred** (K3 reject, GLM53/DSF weakened-draft-insert-only, Sol silent). Not in P1-P3; revisit after P2 preview lands.
- Verification-gate lesson from U1 (2026-08-28): every quality gate must live in `.odo-verify`'s gui line (already: tsc + vitest + playwright), never only in the brief.

## P1 — Quick wins (all S-cost)

1. **P1.1 Journal search wiring** (the orphaned stack): add a typed-only ⌘K palette group "Journal search" — query ≥2 chars → `invoke("search_events", …)` → rows `project · workstream · type · snippet`; Enter = one-flight foreign-switch (reuse `Sidebar.tsx:374-382` path) + open ⌘F prefilled (`App.tsx:201-202` owns searchOpen/searchQuery). Read-only; no new IPC; journal stays the only index.
2. **P1.2 Central selector map**: `gui/src/slots.ts` exporting `SLOT = {composer, statusbar, panelTabs, diffCard, palette, …}`; add `data-slot` attributes alongside existing markers; e2e specs import from it. Do NOT rewrite existing passing selectors — new map adopted at touch points only (typed: one slot per probe consumer).
3. **P1.3 Keybind registry**: `gui/src/keybinds.ts` — typed static table `{id, combo, display, category, allowedInInput}`; App's keydown switch consumes it; `⌘/` opens a read-only shortcuts panel (reuse Settings dialog pattern + Esc stopPropagation); palette renders live `comboFor(actionId)` instead of hardcoded strings.
4. **P1.4 Tool-result inline diffs**: when a tool event's result contains a unified diff, render a compact read-only hunk view via the existing DiffViewer parser; run-group gains a "N files changed" chip row → click opens Changes tab preselected. Journal-derived, zero new IPC. Accept/Reject stays in Changes tab only.
5. **P1.5 Error summarizer map**: `gui/src/errors.ts` — ordered `[pattern → {summary, action?}]` table next to strings.ts; banner shows summary (+ raw string in title) with explicit × dismiss (sticky); toasts keep ambient auto-dismiss.

P1 tests: palette journal-search group (fixture results, Enter switches workstream + prefills ⌘F); slots.ts import-typing; keybind table renders in ⌘/ panel and palette hints match live combos; tool-diff rendering from a fixture event; errors.ts summarization of a fixture error banner. Gates: tsc + vitest + playwright (palette/shortcuts/chat specs).

## P2 — Preview + history + resilience

1. **P2.1 Screenshot inline**: captured `/preview` PNG renders inline via the existing ZoomableImage (scheme-allowlisted data:) on the producing bubble; receipt badge stays as provenance.
2. **P2.2 File-preview panel mode**: new panel tab backed by daemon `read_file`; re-read on activation (existing keep-alive activation-refetch contract); handles artifacts/reports/generated HTML.
3. **P2.3 URL live mode (GUI-initiated only)**: "Open live" action on `preview_captured` badge → panel_overlay posture pane with sandboxed iframe (no allow-same-origin, frame-src limited to `http://localhost:*`/`127.0.0.1:*`). Agent's eyes stay the existing screenshot tool. NO element refs, NO act, NO tour.
4. **P2.4 Runs tab**: ContextPanel "Runs" tab fed purely from journal rows (`run_prompt`/`run_done`/`loop_run_usage`/`auto_panel`): outcome, duration, measured tokens, revise rounds; row click → read-only transcript jump via switch-cache + `data-seq` anchor. Zero new daemon state.
5. **P2.5 Failure taxonomy overlay**: classify poll failure kind from error string; at threshold mount taxonomy panel (Restart / Copy diagnostics JSON: daemon version, poll counters, last errors + daemon log tail via Tauri fs read); banner stays for blips. Esc ladder: modal joins as DOM-class gate.
6. **P2.6 Keep-alive LRU park**: track last-activation per panel tab; park (unmount body, keep tab) beyond cap 3, hidden > N min, and owning no open draft; Memory/Wiki exempt (draft protection exists); activation-edge refetch contract unchanged. Expect review-inbox.spec scoping edits.

P2 tests: inline image renders + lightbox; file tab refetch on activation; URL pane sandbox attrs + localhost-only; runs tab rows from fixture journal + deep-link jump; failure overlay per class + Esc gate; park/unpark lifecycle with draft protection. Gates: tsc + vitest + playwright (panel/statusbar/chat specs).

## P3 — Quality infrastructure

1. **P3.1 Perf baseline gate**: Playwright-driven (NO CDP under WKWebView): two scenarios — keystroke→paint (rAF deltas for 120 chars into mock composer) and panel-tab switch→paint; p50/p95/p99 + slow-16 count exported; committed `gui/perf/baseline.json`; CI-style tolerance gate `limit = baseline*(1+tolFrac)+tolAbs`, exit 1 on regression. Reuses slots.ts hooks.
2. **P3.2 Theme token cascade**: seed vars (`--odo-accent`, bg seeds, mode) → `color-mix()` derived `--ui-*`; migrate token-by-token deleting light-theme duplicates as derived; components untouched (already consume tokens). Unlocks accent setting + third themes.
3. **P3.3 Esc priority registry**: `gui/src/esc_layers.ts` — `pushEscapeLayer(priority, onClose) → disposer`; global handler runs top layer only; migrate the ~12 stopPropagation call sites; keep today's ladder order as the priority constants; composer slash/@ menus mid-priority.
4. **P3.4 Contribution registry (internal only)**: `gui/src/contrib.ts` — three areas: statusbar-chip `{id, order, render, overflowRank}`, panel-tab `{id, label, badge, lazyMount}`, palette-action; migrate existing TABS table + StatusBar chip zone onto it; NO external plugin loading.

P3 tests: perf harness runs and gate exits nonzero on an artificially degraded baseline; theme derivation (light computed from seeds, no duplicate blocks); esc layer ordering (top-most wins, disposer removes); registry migration pins (existing vitest/e2e suites pass unchanged). Gates: tsc + vitest + playwright.

## Order & execution

P1 → P2 → P3, one dispatch each, watcher on each until land. Any panel dissent follows the D7 settle table + revise ladder (vitest gate already inside mechanical verify). After P3: rebuild Odo.app, restart daemon, preview verification of main/sidebar/settings + new surfaces (journal search, runs tab, preview modes, ⌘/ panel).
