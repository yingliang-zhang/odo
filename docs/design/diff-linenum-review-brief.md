# Diff Line Numbers + Split Comments — Tri-Model Review Brief

## 1. Task

Review commit `d3a39ff` ("feat(gui): diff line numbers + split-view comments + file:line comment refs").

Three changes in one commit:
- **#10**: Parse `@@ -oldStart,oldCount +newStart,newCount @@` hunk headers to track real file line numbers; add line-number gutters to inline and split diff views
- **A2**: Fix comment references from `L<diffArrayIndex>` (meaningless to agent) to `file:line` using real line numbers
- **#11**: Add 💬 comment buttons to split view (previously only in inline view)

Files: `DiffViewer.tsx` (+185/-19), `app.css` (+25). 2 files, 262 insertions, 19 deletions.

For each RC, independently verify against the live repo. Give ACCEPT or REJECT.

## 2. Review Criteria

**RC1: Hunk header parsing correctness**
- `HUNK_RE = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/` — verify regex matches standard git hunk format
- `parseHunkHeader` returns `{ oldStart, newStart }` or null
- Verify: handles `@@ -1,5 +1,5 @@`, `@@ -1 +1 @@` (no count), `@@ -10,3 +12,8 @@`
- In `parseInlineRows`: hunk header resets `oldLine` and `newLine` to `oldStart` and `newStart`
- In `parseSplitRows`: same reset logic
- Verify: read the actual code and trace line counter progression through a multi-hunk diff

**RC2: Inline view line numbers**
- Add lines show `newLine` in gutter (green/dim)
- Del lines show `oldLine` in gutter (red/dim)
- Context lines: no line number shown in inline view (only in split)
- Hunk header lines: no line number
- Verify: the `rendered` array builds correctly with `diff-linenum` spans

**RC3: Split view line numbers**
- Left column (old): shows `oldLine` for del rows, `lineNum` for context
- Right column (new): shows `newLine` for add rows, `lineNum` for context
- Padding rows (null cell): no line number
- Context rows in split view: line numbers shown (unlike inline where they're hidden)
- Verify: `renderSplitCell` renders line numbers correctly per kind

**RC4: Comment reference fix (A2)**
- `sendComments` now uses `inlineRows[i]` to look up `filePath` and `newLine`/`oldLine`
- Format: `- file:line: comment` (was `- L<index>: comment`)
- Fallback: `L${i}` when lineNum is null (non-code lines)
- Verify: read the `sendComments` function and trace the comment body construction

**RC5: Split-view comment buttons (#11)**
- Old (left) column cells have 💬 button when `pending && cell.src != null`
- New (right) column cells have 💬 button when `pending && cell.src != null`
- Button `onClick` uses `cell.src` (the diff line index) for `openLine`
- `aria-label` uses `cell.lineNum` when available
- Verify: `renderSplitCell` renders buttons for both old and new kinds

**RC6: Comment state sharing between inline and split**
- Both inline and split views use the same `comments` Map keyed by `srcIndex`
- Both use the same `openLine` state
- The comment box textarea is shared (rendered once at the bottom)
- Verify: clicking 💬 in split view opens the same comment box as inline

**RC7: E2E compatibility**
- 43/43 E2E tests pass
- Diff-related E2E tests (diff.spec.ts) still pass with the new rendering
- Verify: check if any E2E test asserts on diff line content or structure that might break

**RC8: CSS correctness**
- `.diff-linenum` has `width: 3em`, `text-align: right`, `padding-right: 8px`
- `color: var(--text-dim)`, `opacity: 0.55` — subtle, non-distracting
- `font-variant-numeric: tabular-nums` — prevents width jitter
- `user-select: none` — line numbers can't be accidentally selected
- Verify: read the CSS and confirm values

## 3. Instructions

1. Read `gui/src/components/DiffViewer.tsx` and `gui/src/styles/app.css` in the live repo.
2. For each RC1–RC8, independently verify.
3. Trace the line counter logic through a multi-hunk diff example.
4. Verify comment references use real file:line, not array indices.
5. Check split-view 💬 buttons render and wire to the same comment system.
6. Give ACCEPT or REJECT per criterion, then an overall verdict.

Write your complete analysis as text. Do NOT write files to the repository.

## 4. Context

- Tauri 2 + React 18 + Go desktop app (macOS only)
- `DiffViewer.tsx` renders git diffs from the daemon with inline/split toggle
- Comments are fire-and-forget feedback sent to the agent via `onSendComments`
- E2E: 43/43 Playwright tests pass
