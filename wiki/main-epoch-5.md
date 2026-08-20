> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# odo Work Log — 2026-08-19: GUI Gate Repair + `/loop audit` Cap v1.2

Two sessions: (1) repair of the auto-land verify gate blocked by two pre-existing GUI e2e failures; (2) prescriptive amendment raising the `/loop audit` subject cap from 64KB to 256KB.

## Session 1 — GUI e2e gate repair (88/2-fail → 90/90)

**Context.** Pending GUI diff failed the auto-land gate (`cd gui && npx tsc --noEmit && npx playwright test --reporter=line` → exit 1). Both failures reproduced identically on clean main (042ab4b): pre-existing main-debt, but the deterministic gate blocks on them.

**Root causes and fixes (both in `gui/e2e/**` only):**

- **sidebar.spec.ts:103** — copy drift: `strings.sidebar.confirmRemoveTitle` now reads `"Confirm removal"` (strings.ts:89, applied at Sidebar.tsx:550); the test's hand-written literal `"Confirm remove supersplat-hdr"` lagged. Fix: test imports `strings` from `../src/strings` and derives the label from source (0 runtime deps in strings.ts, safe to import), with comment explaining the derivation.
- **diff.spec.ts:21** — not a badge regression: accept is intentionally a **two-step flow** (DiffViewer "Tri-model right sidebar gap"). First `.btn-accept` click only opens an inline commit-message editor prefilled `odo: accept diff #1`; second confirm sends `accept_diff`. Test clicked once → no resolve → no badge. Code intent is badge-on-resolve (`badge badge-accept` + "Applied" on the resolved record card; `ui/badge.tsx` documents those semantic classes as kept for e2e). Fix: test rewritten to the full two-step flow — asserts editor appears with the prefill, confirms, then asserts `.badge-accept`. Rationale documented in the test comment. Reject stays single-step (why the reject test always passed).

**Verification.**
- Clean main worktree: tsc clean, 83/83 pass (fixes stand alone).
- Pending worktree (6a8514d2): tsc clean, **90/90 pass, exit 0**, exact pipeline command; includes all 7 loop.spec.ts additions. Zero flakes across two full runs, no retries.
- Fixes staged into the pending GUI diff; session worktree (6a851f27) reverted clean so identical hunks never land from two worktrees (would conflict at `git apply`).

**Operational incident.** The warned cwd trap reproduced: several `_bash` calls missed the explicit cwd, so `git add` landed in the session worktree instead of the pending one. Corrected by switching to in-command absolute `git -C` / `cd` (not the cwd field) and re-verifying `git status` per worktree.

**Untouched:** no Go files, no dependencies beyond the already-added notification plugin, no `.odo-verify`, no fixture changes.

## Session 2 — `/loop audit` subject cap v1.2 (prescriptive, zero design freedom)

**Context.** Auto-land squashes each accepted diff into ONE commit, so feature-sized audit subjects exceed the settle-shared 64KB cap: M19 impl commit 042ab4b = 233,533B of `git diff base..HEAD`; GUI wave da9923a = 80,889B. `/loop audit base=<m19_base>` self-audit was physically impossible (suspended `subject_too_large` in <1s). Fix: single loop-owned cap of **256KB (262,144B)** — value all three review models (K3/GLM-5.2/DSF-v4-flash blind round) independently chose. The fuller two-tier design (models=single|panel flag, per-tier caps, `findings_too_dense` cause) was explored and **explicitly shelved by the user**; archive at `/tmp/odo-loop-audit-moa/`.

**Changes (exactly as prescribed):**

1. **`internal/ipc/loop_run.go`** — new constant `loopAuditSubjectCapBytes = 256 * 1024` with the prescribed comment (real shapes cited; "convergence guards UNCHANGED"; ">256KB is a hard wall"). `runAuditRound` subject breaker (~L253) now compares against it; findings-feed breaker (~L436) stays on `settleCommentsCapBytes`; suspend detail names the actual cap ("land pending diffs first or narrow the base= range"); cause string `subject_too_large` unchanged. `internal/ipc/settle.go` untouched (protected gate file).
2. **`internal/ipc/loop_test.go`** — two new cap pins following loopRig patterns: ~190KB frozen subject → audit runs (no `subject_too_large`; round row journals `subject_bytes`, budget projection ~160K tokens safe under 2M); ~520KB frozen subject → suspends `subject_too_large` naming 262144. No existing test pinned 64KB loop-subject behavior.
3. **`docs/design/loop-design-lock.md`** — C12 subject clause only: subject capped at `loopAuditSubjectCapBytes` (256KB), loop-owned; findings feed stays at `settleCommentsCapBytes` (16KB). Everything else in C12 untouched.
4. **`docs/milestones/m19-loop.md`** — dogfood line rewritten: self-audit now possible (233,533B < 262,144B); PASS = the loop audits its own code **at all** (a fix verdict converging, or an honest subject/feed suspend with actionable detail) — not necessarily a clean verdict.
5. **`docs/design/loop-audit-cap-v1.2.md`** — NEW, exact provenance content (scope decision, the one change, why 256KB, unchanged guards, dogfood criterion).

**Unchanged guards:** 16KB findings-feed wall (loopSpawnFix), severity gate, infra-is-never-a-verdict, C5 stall, closure-pass, BYOF, one-loop-per-conversation, budget breakers, round cap, land-each-round. Worst case ~64K tokens/leg × 3 legs × 10 rounds approaches budget-breaker near round 8–9 — that projection plus the 500KB hard wall remain the containment.

**Verification.** `go build ./...` ✓, `go vet ./internal/...` ✓, `go test ./internal/ipc/ -run 'Loop' -count=1` ✓ (exit 0).

## Open loops

- GUI e2e mock fidelity gap observed but deliberately left out of scope: `resolveInboxDiff` doesn't update the polled `diffs` fixture, so a badge can be overwritten by pending state on the next poll tick (reject test tolerates the same transient). Not a gate blocker; fix would be a mock-fixture change.
- Dogfood run: `/loop audit base=<m19_base>` on the real 233,533B M19 diff is now admissible under v1.2 but was not executed in-session — criterion defined in m19-loop.md, run pending.
- Shelved two-tier `/loop audit` design (proposals + full DESIGN LOCK draft) archived only under `/tmp/odo-loop-audit-moa/` — ephemeral path; copy into the repo/wiki if the tier design is ever revisited.