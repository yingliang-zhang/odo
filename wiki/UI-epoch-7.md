> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Odo GUI Fixes + Deployment (Diff #12)

## Decisions

- **Right sidebar close X replaced by TopBar toggle** — mirror of left sidebar pattern; ⌘J shortcut unchanged.
- **Popover opacity bug fixed at root cause, not by opacity tweak** — was not a "translucent by design" issue: background was being dropped entirely (`rgba(0,0,0,0)` in browser-computed style).
- **Daemon skipped in redeploy** — diff #12 was GUI-only; Go source untouched, binary already current. Only the Tauri app was rebuilt/swapped/restarted.

## Root cause: fully transparent popovers

- `PopoverContent` base class merges an opaque `bg-[var(--bg-elevated)]`.
- Every StatusBar popover carried an inert e2e marker class `bg-runs-menu`; **twMerge treats any `bg-*` class as the background-color group** and, on conflict, keeps the later one → real background discarded.
- All 5 popovers affected: ctx-meter, panel-chip, auto-land, OMP usage, bg-runs menu.
- Diagnostic path: browser screenshot → computed style `rgba(0,0,0,0)` → recursive @layer traversal of compiled CSS confirmed only `bg-runs-menu` survived.

## Code changes

**Popover fix (`bg-runs-menu` → `runs-menu`)**
- 5 renames in `StatusBar.tsx` + 3 e2e selector updates; comment left in StatusBar.tsx warning not to put `bg-`-prefixed marker classes into `PopoverContent`.
- Post-fix computed style: `rgb(22,26,32)` (opaque `#161a20`).

**Sidebar toggle**
- `ContextPanel.tsx`: removed `panel-close` X button, `onClose` prop, `X`/`Button` imports (dead code removed).
- `TopBar.tsx`: added mirrored toggle right of Settings gear reusing `topbar-nav-btn` chrome — `ChevronRight` when open, `PanelRight` when closed, `title="Toggle panel (⌘J)"`.
- `App.tsx`: passes `panelOpen` / `onTogglePanel`.
- `e2e/panel.spec.ts`: "Close panel" test rewritten as TopBar open/close round-trip.

**Verification**

| Check | Result |
|---|---|
| Browser screenshots (before) | popover transparent, composer bled through |
| Browser screenshots (after) | opaque #161a20; toggle works; no X |
| vitest | 80/80 |
| `tsc --noEmit` | clean |
| playwright full suite | 106/106 |

## Deployment

- Landed via daemon diff pipeline (auto_panel accepted); source = commit `929a62a` (worktree dirty files are wiki artifacts only).
- `tauri:build` (32s) → replaced `/Applications/Odo.app` → relaunched.
  - App sha `e2cf3c14` (21:38) → `6429c3a5` (22:10); PID 90065 → 93950.
  - Bundle audit: `runs-menu` present, `bg-runs-menu` zero residue, `toggle-panel` present.
- Daemons left running: main PID 93445, ui-message-stream PID 90078.
- New on-demand daemon PID 93766 spawned by app for worktree `6a85b74a` (`~/.odo/bin/odo`, holds that worktree's odo.sock) — socket alive, normal per-project spawn, not a stray; left in place.

## Open loops

None.