# Odo GUI Feature Gap Analysis

Cross-app study of Hermes, Multica, Orca, and OpenCode for actionable patterns to adopt in Odo.
Evaluated against first principles: developer convenience, ease of use, performance, clarity.

## Source Verification

| App | Version | Stack | License | Files read |
|---|---|---|---|---|
| Hermes Desktop | local v0.17 | Electron + React + Tailwind v4 + Radix + nanostores | Proprietary | sidebar/projects/{model,workspace-groups,overview-row,workspace-header,project-menu}.ts, session-row-state.ts, chrome.tsx, DESIGN.md |
| Multica | GitHub main | Next.js 16 + Go + Postgres + React Query + Zustand | Source-available | packages/views/issues/components/{board-view,board-card,board-column,execution-log-section}.tsx, packages/ui/styles/tokens.css, packages/views/navigation/context.tsx, CLAUDE.md |
| Orca | GitHub main | Electron + React 19 + Vite + Tailwind v4 + shadcn + zustand | MIT | src/renderer/src/components/{Sidebar,QuickOpen,AgentStateDot,AgentWorkingSpinner}.tsx, sidebar/{WorktreeList,WorktreeCard,smart-attention}.ts(x), store/index.ts |
| OpenCode | GitHub dev | Go + Bubble Tea TUI | MIT | web docs + community guides |

## Patterns to Adopt (ranked by impact/cost)

### 1. Status Dot Priority Reducer
**Source**: Hermes `session-row-state.ts` (35 lines), Orca `AgentStateDot.tsx` + `smart-attention.ts`
**What**: A pure function maps multiple boolean signals (needsInput, isWorking, isStalled, isUnread, hasBackground) into one mutually-exclusive dot state. Hermes: needs-input > working > stalled > background > unread > idle. Orca: blocked > done > working > idle (4-class "smart sort").
**Odo fit**: Currently shows "idle" text and a `.ws-pulse` animation for running. Replace with a colored dot: amber for pending diffs, pulsing accent for running, grey for idle. ~40 lines pure function + one `<span>` per row.
**First principles**: Clarity — one glance tells the developer what needs attention. Performance — pure function, no re-renders.

### 2. Semantic Type Scale Tokens
**Source**: Multica `packages/ui/styles/tokens.css` — `--text-micro` (11px), `--text-caption` (12px), `--text-label` (13px), `--text-body` (14px), `--text-body-lg` (15px), `--text-title-sm` (16px), `--text-title` (18px), `--text-display` (36px). Each with paired `--line-height`.
**Hermes**: `--ui-text-primary/secondary/tertiary/quaternary` (hierarchy, not sizes).
**Odo fit**: Add `--text-caption`, `--text-body`, `--text-label`, `--text-heading` tokens to `:root`. Replace ad-hoc `font-size` declarations. ~15 new CSS vars, mechanical find-replace.
**First principles**: Clarity — named roles prevent "12px vs 13px" ambiguity. Maintainability — one place to change the type scale.

### 3. Sidebar Multi-Project Tree (Overview + Drill-In)
**Source**: Hermes `overview-row.tsx` + `model.ts` — projects show top-3 session preview, click to drill in; Orca `WorktreeList.tsx` — project groups with worktree children, virtualized via `@tanstack/react-virtual`.
**Odo fit**: Replace dropdown project picker with a collapsible tree. Active project expanded with workstreams; others collapsed with lazy-fetch on expand. Back row to return to overview. Odo's workstreams ≈ Hermes sessions (not worktrees).
**First principles**: Developer convenience — see all projects + status at a glance, no dropdown hunting. Performance — lazy fetch, only load workstreams on expand.

### 4. Hover-Revealed Context Actions (Menu Items as Data)
**Source**: Hermes `project-menu.tsx` — `useProjectActions` hook returns `ActionItemSpec[]` rendered identically into kebab menu and right-click context menu. Orca `WorktreeCard.tsx` — `WorktreeContextMenu` with same pattern.
**Odo fit**: Replace always-visible Pencil/Trash2 buttons (F7) with hover-revealed kebab. Single `useWorkstreamActions()` returns action list. Same actions in kebab and right-click.
**First principles**: Clarity — less visual noise when not hovering. Consistency — one action set, two entry points. Maintainability — add an action once, it appears in both menus.

### 5. Tail-Pin Truncation for Long Names
**Source**: Hermes `workspace-header.tsx` `LaneLabel` — truncates the head, pins the tail (last 14 chars) so `feat/sidebar-tree-rename` shows `…tree-rename` not `feat/sidebar-t…`.
**Odo fit**: Apply to workstream names in sidebar. ~10 lines, a `<span>` with `truncate` head + `shrink-0` tail.
**First principles**: Clarity — long branch/workstream names stay distinguishable. Developer convenience — `feat-foo-bar-baz` and `feat-foo-bar-qux` don't both collapse to `feat-foo-…`.

### 6. Persisted Collapse State per Node
**Source**: Hermes `useWorkspaceNodeOpen` — `Record<string, boolean>` in localStorage; resolved-absolute writes (a flipped default never clobbers user choice).
**Odo fit**: Remember which projects/workstreams are expanded. ~20 lines, `localStorage.getItem`/`setItem` keyed by `odo:sidebar:open:<id>`.
**First principles**: Developer convenience — sidebar state survives restart. Performance — localStorage read, no daemon call.

### 7. Stroke Token Hierarchy
**Source**: Hermes DESIGN.md — `--ui-stroke-primary…quaternary` for hairlines in descending strength; `--ui-stroke-tertiary` as the default in-panel divider.
**Odo fit**: Replace single `--border` with `--stroke-primary` (strongest, panel edges), `--stroke-secondary` (default dividers), `--stroke-tertiary` (subtle, in-list hairlines). ~6 CSS var renames.
**First principles**: Clarity — visual hierarchy via stroke strength, not color guessing. Maintainability — one token per elevation level.

### 8. Token Taxonomy Enforcement Rule
**Source**: Hermes DESIGN.md — "tokens, not literals; if you reach for a raw color, stop — there's already a token for it." Enforced by convention + test (`title=` attribute detection).
**Multica**: `tokens.css` documents the exact type scale with rationale for each step.
**Odo fit**: Write a one-page design contract in `docs/design-tokens.md` naming every CSS var, its role, and the "never use a literal" rule. Review checklist item for future changes.
**First principles**: Maintainability — conventions decay without a doc. Clarity — new contributors know what to use.

## Patterns Considered but NOT Adopting

| Pattern | Source | Why skip |
|---|---|---|
| Worktree/git-branch lanes | Hermes `workspace-groups.ts:300-754` | Odo workstreams ≠ Hermes worktrees; data model mismatch |
| Kanban board | Multica `board-view.tsx` | Odo has no cross-workstream board concept (yet) |
| dnd-kit drag reorder | Hermes + Orca + Multica | ≤20 workstreams; HTML5 drag or ↑/↓ sufficient |
| Cron jobs section | Hermes `cron-jobs-section.tsx` | Odo has no scheduling |
| Profile switcher | Hermes | Odo is single-profile |
| Terminal multiplexer | Orca `xterm` stack | Out of scope for Odo |
| react-virtuoso / @tanstack/virtual | Multica + Orca | No measured scroll jank yet; defer to >500 messages |
| framer-motion | — | None of the 3 apps use it; CSS animations suffice |
| Tailwind CSS | Orca + Multica | Odo has working CSS var system; migration is churn |
| cmdk | Orca | Hand-built palette works; add when fuzzy search needed |
| sonner | Orca + Multica | Hand-built toasts work; add when stacking/promise needed |

## Implementation Plan (Phase 3)

| Step | Pattern | Files | Est. lines |
|---|---|---|---|
| 3.1 | Status dot reducer | Sidebar.tsx, app.css | ~60 |
| 3.2 | Semantic type tokens | app.css (:root + light theme) | ~20 |
| 3.3 | Stroke token hierarchy | app.css | ~15 |
| 3.4 | Tail-pin truncation | Sidebar.tsx | ~15 |
| 3.5 | Persisted collapse state | Sidebar.tsx | ~25 |
| 3.6 | Hover-revealed context actions | Sidebar.tsx, app.css | ~80 |
| 3.7 | Multi-project tree | Sidebar.tsx, App.tsx | ~150 |
| 3.8 | Design token contract doc | docs/design-tokens.md (new) | ~80 |
| **Total** | | | **~445 lines** |

## Verification

- Browser dev mode (`npm run dev` + mock fixtures) for all visual iteration
- tsc + vite build after each step
- Side-by-side comparison with Hermes `dev:mock` in Electron
- Tri-model audit-review-loop after all steps complete
