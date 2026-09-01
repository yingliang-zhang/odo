> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# UX-4 — ContextPanel tab diet (ledger folds into Runs, per ux-batch-lock-amendment-a3)

## Scope & user rulings
- Authoritative spec: `docs/design/ux-batch-lock-amendment-a3.md`. Base: `main @ 4c668f5` (UX-2 landed). Work done in a dedicated worktree; staged, not committed, per task gates.
- User-ruled dispositions (A3-1, final): **ledger folds**, **learning stays (trial-bound)**, **runs stays**. No behavior changes to learning/runs/preview beyond the ledger fold.

## Key decisions
- **Delete, don't shim**: removed the `ledger` entry from `CONTRIBUTIONS` in `contrib.ts`; `PanelTab` union + `PANEL_TAB_IDS` re-derive automatically and the compiler enumerated every reference site (App.tsx mount block, `openPanelTab("ledger")` call sites, keep-alive freeze, localStorage allowlist comment) — all cleaned, no aliases left.
- **LedgerPanel.tsx → ReviewReceipts.tsx** (git detects rename): `ReviewRow` + risk/timed-out/run-log machinery moved wholesale; exported `reviewReceipts()` (filter+sort). Receipts mount inside the Runs tab's keep-alive wrapper — no thin LedgerPanel shell kept. `runsEventsRef` now drives receipts; the separate `ledgerEventsRef` freeze is gone.
- **Deep links rewired to runs**: FailureOverlay "Open Journal" → `openPanelTab("runs")`; "ledger write failed" toast → `openPanelTab("runs")`.
- **TopBar overflow "Ledger"** retargets from the dead tab to `handlePreviewFile(".odo/ledger.md")` (Preview pathway); label and icon kept.
- **Measure-then-assert**: live-measured strip supersedes A3-3's 655/659 estimates — 9 tabs ≈ 665px content vs 703px strip client at 720px MAX → **no-arrow fit restored** (controls unmount; direction per doc was right, actual margin 38px). MIN 280px clips ≈ 446px. Probe methodology: settle a frame before reading overflow (first probe raced the React commit and reported stale arrows); read CSS values in `px` (unit-less values dropped by CSSOM).
- **Spec flip**: `context-panel-tabs.spec.ts` test-2 inverted to "controls **disappear** at MAX, every tab visible at rest" (holds within the 1440px viewport); test-1 rightmost tab Ledger → Learning.
- **Dead code cut**: `api.ledger()`, `LedgerResponse` (types + fixtures import/re-export), mock-invoke `ledger` case. Daemon-side `ledger` IPC command deliberately retained (CLI surface untouched) — frontend consumption only.
- Worktree `node_modules` provisioned via APFS clone from the main checkout (worktree skill).

## Code changes (16 files, +351/−330, staged, HEAD `4c668f5`)
- `contrib.ts` — ledger entry removed; header comment carries the measured budget rule (9 tabs ≈ 665px vs 703px client at MAX).
- `contrib.test.tsx` — strip-budget assertion `expect(PANEL_CONTRIBUTIONS.length).toBeLessThanOrEqual(9)`.
- `ReviewReceipts.tsx` (new, rename) — moved row machinery + `reviewReceipts()`.
- `RunsPanel.tsx` — "Receipts" section (title preserved: "review actions — journal receipts, newest first"); joint empty state `runs.length === 0 && receipts.length === 0`.
- `App.tsx` — ledger tab mount + keep-alive block deleted; deep links → runs.
- `TopBar.tsx` — overflow Ledger item → `handlePreviewFile(".odo/ledger.md")`.
- api/types/fixtures/mock — ledger surface removed.
- Unit tests: `runspanel.test.tsx` +3 receipts tests (ordering, risk badges / unrated / timed-out / run-log, bookkeeping exclusion, joint empty state); `app_keepalive` ledger case removed + **orphan fallback proof** (stored `"odo-panel-tab"="ledger"` → falls back to `"tasks"` via existing allowlist, aria-selected assertion); strip-order assertions: tasks, changes, review, wiki, memory, skills, runs, preview, learning (9; ledger absent).
- E2E: `ledger.spec.ts` rewritten — receipts render inside Runs tab (`.ledger-review-row` class names kept as identity hooks), TopBar overflow → Preview opens ledger.md, strip has 9 tabs with no Ledger; `lru-park`/`panel` specs deep-tab Ledger → Runs; `boot.spec` comments synced.
- Post-staging fix: a footer comment still cited the deleted LedgerPanel — corrected and restaged.

## Verification (gate tails)
| Gate | Result |
|---|---|
| `tsc --noEmit` | clean |
| vitest (full) | 39 files / 462 tests green; **3 full runs** — first had 5 timer-sensitive failures (cold cache after APFS clone; env 221s vs normal 118s), next two green, repro 0, failures confined to untouched files |
| Playwright gated subset (tabs/lru/ledger/boot/panel, `--retries=1`) | 15/15 (58s) |
| Stability (tabs+ledger, `--retries=0 --repeat-each=2`) | 14/14 ×2, zero retries |
| `git diff --cached --check` | clean; `:1420` released |

Live browser evidence: at MAX width arrows absent (`arrows: 0`) and the Learning tab fits inside the strip (1394 ≤ 1432).

## Open loops
- Worktree is staged but **uncommitted** (16 files, +351/−330, HEAD `4c668f5`); review panel blocked auto-land (`verify_failed`) — landing/commit awaits explicit user review.
- First vitest run's 5 timer-sensitive failures: repro 0, attributed to machine-load flake on unchanged files; decide accept-as-flake vs. investigate.
- UX-2b's jobs tab will push the strip back to 10 — A3-3 explicitly accepts the arrows posture then; the `≤ 9` budget assertion and contrib.ts header comment must be recalibrated at that point (noted in code).
- A3-3 doc's 655/659 estimates are superseded by measured 665/703 — confirm whether the doc itself needs a correction pass (correction currently recorded in code comments).
- Daemon-side `ledger` IPC command intentionally retained (CLI surface untouched) — future removal is a separate, unrequested decision.