# PR3 TopBar Declutter — Tri-Model Review Brief

## 1. Task

Review commit `48aebf1` ("feat(gui): PR3 TopBar declutter — Distill + ⋯ overflow + Settings gear").

This commit declutters the TopBar from 6 labeled buttons to 3 visible controls: Distill (labeled), ⋯ overflow menu, Settings (gear icon only). Wiki/Curate/Pin/Ledger moved into the ⋯ overflow dropdown. 3 files changed, 241 insertions, 44 deletions.

For each RC, independently verify against the live repo. Give ACCEPT or REJECT with evidence.

## 2. Key Diff Summary

**TopBar.tsx** — Major restructure:
- Added `IconButton` component (icon-only, no label span)
- Added `overflowOpen` state + `overflowRef` for outside-click detection
- Added `useEffect` for outside-click + Escape to close overflow menu
- Removed: Wiki, Curate, Pin, Ledger as direct ActionButton elements
- Added: ⋯ overflow button (`MoreHorizontal` icon, `aria-label="More actions"`, `aria-haspopup="menu"`, `aria-expanded`)
- Overflow menu items: Curate, Pin, separator, Wiki (with badge), Ledger
- Pin popover moved outside the overflow div (conditionally rendered when `pinOpen`)
- Settings changed from `ActionButton` (with label) to `IconButton` (no label)

**app.css** — New selectors:
- `.topbar-action-icon-only` (tighter padding, hide label)
- `.topbar-overflow` (position: relative container)
- `.topbar-overflow-menu` (absolute dropdown, frosted vibrancy, z-index 60)
- `.topbar-overflow-item` (flex row, hover state with accent tint)
- `.topbar-overflow-sep` (1px divider)
- `.topbar-overflow-badge` (right-aligned count badge)

**boot.spec.ts** — Updated E2E:
- Old: asserted 6 `.topbar-action` with hasText: Distill/Wiki/Curate/Pin/Ledger/Settings
- New: asserts Distill (hasText), More actions (aria-label selector), Settings (aria-label selector), then opens overflow and asserts Curate/Pin/Wiki/Ledger inside `.topbar-overflow-menu`

## 3. Verification Evidence

- `go build ./...` → PASS
- `npx tsc --noEmit` → PASS
- `npm run build` → PASS, CSS 53.96 kB
- `npx playwright test` → **43/43 PASS**

## 4. Review Criteria (RC1–RC8)

**RC1: TopBar has exactly 3 visible action controls**
- Distill: labeled ActionButton with icon + text + badge
- ⋯ overflow: IconButton with MoreHorizontal icon, aria-label="More actions"
- Settings: IconButton with Settings icon, aria-label="Settings (⌘,)"
- Verify: read TopBar.tsx and confirm only these 3 are direct children of `.topbar-actions` (plus the overflow menu dropdown and pin popover which are conditional)

**RC2: Overflow menu contains Curate, Pin, Wiki, Ledger with correct actions**
- Curate: calls `handleCurate()` after closing menu
- Pin: calls `togglePin()` after closing menu (opens pin popover)
- Wiki: calls `onOpenWiki()` after closing menu (opens panel wiki tab)
- Ledger: calls `onOpenLedger()` after closing menu (opens panel ledger tab)
- Separator between Pin and Wiki (`.topbar-overflow-sep`)
- Verify: each overflow item has correct onClick, correct disabled state, correct title

**RC3: Overflow menu close behavior**
- Outside click closes the menu (mousedown listener on document)
- Escape key closes the menu
- Clicking any menu item closes the menu (setOverflowOpen(false) before action)
- Verify: the useEffect cleanup properly removes listeners; the overflowRef correctly contains the overflow button + menu

**RC4: Pin popover still works after restructure**
- Pin popover is now conditionally rendered when `pinOpen` is true (outside the overflow div)
- Pin text input, submit button, error display all preserved
- Escape in pin input closes pin popover (not overflow menu)
- Verify: the pin form, handlePinSubmit, togglePin are unchanged from the original code

**RC5: E2E selector compatibility**
- `boot.spec.ts` now uses `aria-label` selectors for icon-only buttons (More actions, Settings)
- `shortcuts.spec.ts:59` still works: Settings dialog opens via `⌘,` → `role="dialog"` + `aria-label="Settings"` unchanged
- `panel.spec.ts` Wiki/Ledger tabs still work (they're in ContextPanel, not TopBar)
- Verify: all 43 E2E tests pass (evidence says they do)

**RC6: Accessibility**
- ⋯ button has `aria-haspopup="menu"` and `aria-expanded={overflowOpen}`
- Overflow menu has `role="menu"`, items have `role="menuitem"`
- Icon-only buttons have `aria-label` matching their title
- Verify: all interactive elements have accessible names

**RC7: CSS visual correctness**
- `.topbar-overflow-menu` is absolute positioned, right-aligned, z-index 60
- Frosted vibrancy: `backdrop-filter: blur(16px) saturate(160%)` + `-webkit-` prefix
- Hover state: `color-mix(in srgb, var(--accent-user) 10%, transparent)`
- `.topbar-overflow-sep` is a 1px divider with `--stroke-tertiary`
- `.topbar-overflow-badge` is right-aligned via `margin-left: auto`
- `.topbar-action-icon-only` hides the label span via `display: none`
- Verify: read the CSS and confirm all values are correct

**RC8: No dead code or stale selectors**
- The old `.topbar-pin` wrapper div is now conditionally rendered — check if the CSS still makes sense
- No orphaned CSS selectors from the old 6-button layout
- Verify: search for any `.topbar-action` CSS that references labels that no longer exist

## 5. Instructions to Reviewers

1. Read `gui/src/components/TopBar.tsx`, `gui/src/styles/app.css`, and `gui/e2e/boot.spec.ts` in the live repo.
2. For each RC1–RC8, independently verify.
3. Check that the overflow menu opens, shows 4 items + separator, and closes correctly.
4. Verify the pin popover still works after being moved outside the overflow container.
5. Check that icon-only buttons (⋯ and Settings) have correct aria-labels.
6. Give ACCEPT or REJECT per criterion, then an overall verdict.

Write your complete analysis as text in your response. Do NOT write files to the repository.

## 6. Context

- Tauri 2 + React 18 + Go desktop app (macOS only)
- TopBar is 38px high with frosted vibrancy background
- ContextPanel already has Wiki and Ledger tabs — this PR just removes the TopBar shortcuts
- Command palette (⌘K) still has all actions (Distill/Curate/Pin/Wiki/Settings) — unchanged
- DESIGN LOCK: TopBar simplified to 3 visible + overflow menu
