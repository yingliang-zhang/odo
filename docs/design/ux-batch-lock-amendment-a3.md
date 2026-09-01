# UX Batch Lock Amendment A3 — ContextPanel tab diet (2026-09-01)

Quad-blind review: K3/GLM/DSF/Sol = 4/4 ACCEPT_WITH_CHANGES (leg outputs
archived in docs/reviews/2026-09-01-tab-diet/, copied from
/tmp/odo-diet-review). USER RULING 2026-09-01: ① Ledger folds
(consensus 4/4); ② Learning stays AS-IS until the LEA-1/SKL-1 trial
concludes (Sol/DSF posture — trial-blind removal is the one
irreversible harm); ③ Runs stays (3/4 vs K3's merge-into-Jobs).

## A3-1 — Dispositions (the ruled set)

| Tab | Ruling | Destination |
|---|---|---|
| tasks, changes, review, wiki, memory, skills, preview, runs | KEEP | unchanged |
| learning | KEEP (trial-bound) | post-trial decision rides the trial doc's own revert condition; if candidates flow it earns badge+slot, if confirmed-zero it folds into Memory (K3's healing argument, LearningPanel.tsx:8-10) |
| **ledger** | **OUT of strip** | receipts → Runs section; ledger.md file → Preview |
| jobs (UX-2b, upcoming) | ADD when k8s configured | A2-5 unchanged (Container icon) |

## A3-2 — Ledger fold mechanics (GLM's split, both halves)
1. Review receipts (ReviewRow + risk badges + run logs,
   LedgerPanel.tsx:123-272) → one `<section>` inside RunsPanel; the two
   folds are adjacent journal refs already (App.tsx:699-706
   ledgerEventsRef/runsEventsRef). Row renderer moves wholesale.
2. ledger.md file view → the Preview tab's existing file pathway:
   TopBar overflow "Ledger" retargets to handlePreviewFile(".odo/ledger.md")
   (App.tsx:1857-1862 pattern). Preview is the P2.1-locked file surface;
   a daemon-written metrics file is a file.
3. Retarget the 3 deep-links: FailureOverlay "Open Journal" (App.tsx:1930)
   → runs; TopBar overflow Ledger item (TopBar.tsx:268-276) → preview
   file open (retitle stays "Ledger"); "ledger write failed" toast
   (App.tsx:2425) → runs.
4. contrib.ts: DELETE the ledger entry. PANEL_TAB_IDS/PanelTab re-derive
   (compiler enumerates every reference site — no shim). Stored
   localStorage "odo-panel-tab"="ledger" orphans → allowlist fallback
   to "tasks" (App.tsx:183-189; anticipated by contrib.ts:60-62 comment).
5. Left sidebar bottom: STAYS EMPTY. 4/4 rejected as host (48px collapsed
   rail renders only the expand chevron, Sidebar.tsx:703-714; 240px
   cannot honestly hold tables/editors). Reserved for future passive
   pulse content (env summary class) — one ADR line, no code.
6. Settings: observability never hides there (taxonomy verdict 4/4).

## A3-3 — Strip budget rule (DSF)
contrib.ts header comment + a vitest assertion in contrib.test.tsx:
CONTRIBUTIONS.length must stay ≤ 9 (the measured no-arrow fit at 720px
MAX is 9 tabs ≈ 655px ≤ 659px client; 10 ≈ 730px overflows at rest).
Post-diet strip = 9 (8 + learning) → jobs arrives = 10 only while
k8s is configured (A2-1 off-by-config ⇒ no tab, so the budget holds by
construction in the no-k8s state; when k8s is ON the strip is 10 =
arrows posture at MAX, accepted by this amendment explicitly).

## A3-4 — Sequencing
UX-2 (in flight: chip + IPC + UX-3a) does NOT touch contrib. The diet
lands as UX-4 after UX-2: contrib.ts ledger delete + LedgerPanel fold
into Runs + Preview retarget + deep-link rewires + budget test + e2e
ledger.spec.ts rewrite (modal→none; receipts assertions move to
runspanel tests; ledger.md assertions move to preview tests).
Post-trial learning decision = separate amendment when trial closes.
