# UI-UX DESIGN LOCK — Layout & Typography Hardening (2026-08-28)

Four-leg blind audit (kimi-k3 / glm-5.3-flash / deepseek-v4-flash / gpt-5.6-sol, sealed 900s, thinking max) against audit brief `/tmp/odo-ui-audit-brief.md`. Skeleton health confirmed 4/4 (zero stretch hacks, min-w-0/min-h-0 discipline intact). No P0. User rulings on the three divergence points:

- ② StatusBar overflow strategy: **hide-by-priority** (3:1 vs horizontal scroll)
- ① Narrow-window guard trigger: **measured chat width** (K3/DSF: ResizeObserver + hysteresis), NOT window-width breakpoints
- ③ Reading measure: **prose-cap** (K3/DSF: keep 1100px column, cap prose blocks inside bubbles only), NOT column shrink

Implementation split (user-approved): **two diffs, U1 then U2**, both `gui/**` only, both through the Odo auto-land pipeline (verify = tsc + vitest + playwright; 3-leg panel; Tier-1 attestation).

## U1 — Low-risk quick wins (StatusBar + tokens + small fixes)

Binding spec, in order:

1. **U1.1 StatusBar hide-by-priority overflow** (StatusBar.tsx + app.css): footer gets `overflow-hidden`; chips keep `whitespace-nowrap shrink-0`. Priority classes (hide FIRST → LAST): ctx-meter → OMP → Panel×N → pipeline/running → count chips (pending diffs / wiki / memory proposals) — actionable chips are hidden LAST; read-only telemetry FIRST. Hidden set collapses into a single `+N` chip (click → Radix popover listing the hidden chips with their live values, same interaction pattern as the TopBar ⋯ overflow menu). Measurement: reuse the ContextPanel tabsOverflow pattern (ResizeObserver + debounced re-measure). **e2e invariant: `status-badge` hooks stay MOUNTED — visibility class toggles only, never unmount.**
2. **U1.2 Type-token sweep** (StatusBar.tsx, app.css): STATUS_BADGE `text-[10px]` → `text-micro` (11px token); grievance badge `text-[9px]` → 10px via a new `--text-nano: 10px` token; `.queue-next-tag` 9px → `--text-nano`; `.mono` 12px, `.topbar-action` 12px, `.settings-title` 17px, `.diff-toggle` 13px map to nearest existing tokens. Do U1.1 and U1.2 in ONE diff (chip widening changes the overflow math).
3. **U1.3 Delete global `.truncate` cap** (app.css:262-267): remove the unlayered `max-width:160px` (and duplicate ellipsis props — Tailwind's layered `truncate` utility supplies them). Explicitly re-cap where a cap is actually wanted: slash-menu rows and mention-menu rows get `max-w-[220px]`.
4. **U1.4 Composer focus fixes** (app.css, ChatSurface.tsx, ModelPill.tsx): scope the outline kill to `.chat-input textarea:focus { outline: none }` (was `:focus, :focus-visible` on ALL descendants — killed ModelPill trigger and menu-item focus). Composer focus-within border: `border-[color-mix(in_srgb,var(--accent-user)_55%,transparent)]` (≤60% tint, does not compete with user bubble accent). ModelPill trigger gains `focus-visible:ring-2` per CVA Button convention.
5. **U1.5 Toast z-order** (app.css): `.toast-viewport` z-index 90 → 95 (above panel overlay z-90, below Radix layers 100+).

U1 tests: StatusBar priority ordering (narrow viewport → telemetry chips hidden first, count chips last, +N popover opens with correct live values); e2e `status-badge` still queryable when hidden; `.truncate` removal → StatusBar identity uses full available width; textarea focus has no outline, ModelPill trigger HAS visible focus ring; toast above overlay panel at ≤999px (jsdom z-index assert). `npx tsc --noEmit` + `npx vitest run` + targeted Playwright specs (statusbar/sidebar/panel).

## U2 — Responsive guard + reading measure (bigger behavior changes)

1. **U2.1 Chat-width-driven panel overlay with hysteresis** (App.tsx + ContextPanel.tsx + app.css): ResizeObserver on `.app-main` measures REAL chat width. When chat < 560px → panel switches to overlay geometry (existing fixed-overlay style, + scrim + close affordance). Return to docked at chat > 600px (40px hysteresis band; evaluate only on resize transitions, not per-frame). **DELETE the `max-[999px]:fixed` window-width breakpoint — one mechanism, not two.** Panel overlay posture: add a click-through scrim (`bg-black/20`) and the existing ⌘J/panel-toggle closes it. Drag clamp fix: `MAX_WIDTH = min(720, window.innerWidth - sidebarWidth - 400)`; align JS clamp 280–720 with the CSS (kill the dead 600/720 mismatch).
2. **U2.2 Panel width persistence** (ContextPanel.tsx): persist `panelWidth` to localStorage key `odo-panel-width` (clamped on read), same pattern as sidebar-collapse persistence.
3. **U2.3 Panel default width** (app.css): `--panel-width: 380px` → `420px` (measured tab-strip scrollWidth 419–457px at 380px — arrows were the resting state; 420 fits the worst measured case minus chrome tightening). Only the DEFAULT; user drag/persistence still wins.
4. **U2.4 Prose measure cap in chat bubbles only** (app.css): `.bubble .bubble-text :where(p, li, ul, ol, blockquote, h1, h2, h3, h4) { max-inline-size: 72ch }`; `pre, table, code` blocks exempt. Do NOT touch Markdown.tsx globally (WikiBrowser keeps its own measure). `--chat-column-width` stays 1100px.
5. **U2.5 Bubble timestamp reservation trim** (MessageBubble.tsx): `pb-[26px]` → `pb-[20px]` (button true footprint ~18px). Keep hover-reveal behavior.

U2 tests: hysteresis state machine (mock ResizeObserver widths: 700→540→580→620 transitions assert overlay on/off/on with no oscillation at 590/600); `max-[999px]` selector no longer exists in ContextPanel.tsx; panel width persists across unmount/remount; default width 420 without stored key; prose cap present on chat bubble text blocks and absent on wiki markdown; pb-[20px] on bubbles. tsc + vitest + Playwright (sidebar/panel/statusbar specs + a 900px-viewport overlay test).

## Deferred (logged, not this wave)

TopBar min-width margin auto (DSF); sidebar transform-based animation rework (Sol); classic-scrollbar tab-strip track (K3, Win/Linux only); fading mask on panel tabs (Sol).

## Order & gates

U1 first (quick wins), U2 second (needs its own e2e round). Both land via the normal pipeline; any panel dissent follows the D7 settle table (single reject → suspend, ≥2 families → auto-reject). After both land: rebuild Odo.app, restart daemon, visually verify main panel / sidebar / settings sub-panels in the live app + Hermes preview.
