> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# UX-1 patch re-apply (diff #132) and default-tab ripple fixes (diff #133)

## Background

UX-1 (topbar alignment + new Tasks panel tab; lock D2, `docs/design/ux-batch-lock-2026-09-01.md`) flipped the ContextPanel default tab `changes` → `tasks` (`App.tsx` useState init + `panelTabRef`). Two landing attempts were blocked by the auto-panel (`verify_failed` → reject):

1. #132's verify failed 3 e2e specs — root-caused as **harness contamination, not patch defects**: `playwright.config.ts` `reuseExistingServer: true` reused a stale dev server on `:1420` serving unpatched main. Re-run with the stale server killed: 4/4 green (/tmp/ux1-diag, 2026-09-01 12:26–12:31).
2. Verify #133 of the re-applied tree then exposed **6 real ripple failures** — specs that open the panel (Meta+j) and immediately assert tab-body content now land on Tasks.

## Task 1 — re-apply diff #132 (single action, no edits)

- Patch path from journal: `.odo/diffs/6a964aed-6d84ef61cd23.diff`; `git apply --3way` clean (3 new files via direct fallback, normal).
- Port 1420 confirmed free before gates (no stale server this time).
- Exactly 13 files — 3 A (`TasksPanel.tsx`, `TasksPanel.test.tsx`, `TodoList.tsx`), 10 M (`App.tsx`, `ChatSurface.tsx`, `ContextPanel.tsx`, `PlanChip.tsx`, `contrib.ts`, `contrib.test.tsx`, `styles/app.css`, `playwright.config.ts`, `e2e/context-panel-tabs.spec.ts`, `e2e/lru-park.spec.ts`).
- Gates: `npx tsc --noEmit` exit 0; `npx vitest run` 37 files / 436 tests; playwright `lru-park` + `context-panel-tabs` 4/4 (29.4s) with a freshly cold-booted server on :1420.
- Anomalies root-caused, neither a patch defect:
  - **vitest first round 8 fails**: cold-cache I/O starvation (setup 461s, import 1263s vs warm 13s/31s) → timeout-shaped errors; two consecutive green runs (26.6s) confirmed.
  - **playwright "two versions of @playwright/test"**: `npx` invoked from the worktree **root** hit the npx global-cache runner alongside the worktree-local install; spec args also substring-matched with no config at root. Fix: run from `gui/`. The node_modules symlink built during diagnosis was replaced with an APFS-clone real directory + vite cache cleared — later proved unnecessary, harmless, gitignored.
- Result: 13 files staged, dirty, zero commit.

## Task 2 — fix UX-1 ripples (6 specs + auto-open landing rule)

Premise correction: the assigned "fresh worktree with #133 already applied" was actually clean — the agent applied #133 itself from the journal patch (13 files, same content as #132).

### Fix 1 — spec recalibration (assertions not weakened)

- `e2e/diff.spec.ts` (:21 accept, :42 reject, :53 review): new `openChangesTab` helper (Meta+j → click Changes) since UX-1 lands on Tasks by default.
- `e2e/diff.spec.ts:11`: rewritten as the landing-rule e2e (below).
- `e2e/pipeline-chip.spec.ts:213`: click Changes after Meta+j before asserting.
- `e2e/runs.spec.ts:123`: real root cause was **keep-alive hidden-body interference** — Tasks (registry-first) mounts at boot; its hidden body also carries `.mem-body`, and the old `.first()` locator hit it. Fixed by scoping to the active body: `.panel-body > div:not([hidden]) .mem-body` (RunsPanel renders `.mem-body` in both empty/rows branches, so the mount-path contract is unchanged).
- Repo-wide audit of all Meta+j call sites: these 6 were the complete set; remaining specs click tabs first or assert only the panel shell.

### Fix 2 — auto-open landing rule: already implemented in HEAD

`App.tsx:1018–1038` (M9 P2 transition) already calls `setPanelOpen(true); openTab("changes")` inside the `!panelOpenRef.current && 0→1` branch — panel-closed lands on Changes; already-open does not yank the tab. **No duplicate code added**; only the missing e2e pin at `diff.spec.ts:11`: drive a real 1→0→1 via a new mutable `changesDiffs` list in `fixtures.ts` (clear → wait for status-bar `[data-chip="diffs"]` to disappear, which latches because it is poll-derived → re-add), then assert the panel opened itself and Changes has `aria-selected=true` — zero keypresses.

### Two real defects found during gating and fixed (beyond literal task scope)

1. **Bootstrap re-mount race** (product): `applyBootstrap` cleared `diffs=[]` and the first poll refilled it — the Changes tab flipped between singular/list render branches ~1 poll after boot, unmounting/remounting and destroying editor-local state (Playwright "element was detached"); accept/review flows dead under parallel load. Fix: bootstrap seeds `setDiffs(resp.diff ? [resp.diff] : [])` so the first poll is content-equal. (This was also the root cause of a first-attempt review-test flake: `.review-results` appeared but stayed empty.)
2. **Accept/reject badge + mock fidelity**: the Changes tab renders from the `diffs` list, but `handleAccept`/`handleReject` only updated singular `setDiff` — the badge never appeared. The e2e mock's accept/reject also didn't flip poll-side state, so the next poll reverted optimistic status. Fixed both sides: App updates `setDiffs` optimistically; the mock flips the matching `changesDiffs` row.

### Process incidents

- One self-inflicted sparse-edit accident: a fuzzy splice in `pipeline-chip` swallowed the Meta+j line; trace proved the keypress never fired; line restored verbatim.
- The Bash tool's `cwd` parameter intermittently dropped from calls; worked around with explicit subshells and absolute paths (all gates run from `gui/`).

### Gates (all green, run from `gui/`, :1420 cleared first)

- `npx tsc --noEmit` → exit 0
- `npx vitest run` → 37 files / 436 passed
- `diff.spec --retries=0 --repeat-each=5 --workers=3` → 20/20 (re-mount race dead)
- `pipeline-chip:213 --repeat-each=3` → 3/3
- Final 10-spec gate (diff×4, pipeline-chip×1, runs×1, lru-park×2, tabs×2) → **10 passed (40.1s)**, retries=1 with zero retries used

### Landed state

18 files staged, zero commit, left dirty in the agent's own worktree: #133's 13 + 5 newly touched (`fixtures.ts`, `mock-invoke.ts`, `e2e/diff.spec.ts`, `e2e/pipeline-chip.spec.ts`, `e2e/runs.spec.ts`; `App.tsx` already within the 13). `git diff --cached --check` clean, no test-results mixed in.

## Durable practices re-confirmed

- Kill any stale dev server on :1420 before e2e: `lsof -ti :1420 | xargs kill -9 2>/dev/null; sleep 2`.
- Invoke `npx`/playwright from `gui/`, never the worktree root (dual `@playwright/test` instance pitfall).
- Cold-cache vitest first-run failures are timeout-shaped — re-run before diagnosing.
- Stage in own worktree, leave dirty, zero commits; the pipeline stages/lands.

## Open loops

- Pipeline verify / auto-land of the 18-file staged set: both prior attempts were blocked (first on contamination — now root-caused and cleared; second on the six real ripple failures — now fixed with a 10/10 gate).
- The two beyond-scope defect fixes (bootstrap re-mount race; accept/reject badge + mock fidelity) are staged together with the ripple fixes — confirm they land in the same change set or get separated for review.
- `runs.spec.ts:123` was fixed by locator scoping (`.panel-body > div:not([hidden]) .mem-body`) rather than the instructed mechanical "click the Runs tab first" — confirm the approach is accepted (assertions unchanged).
- The task-2 premise "fresh worktree with #133 already applied" did not hold (worktree was clean); the agent applied #133 manually — worth checking whether the patch-pre-provisioning step upstream is broken.