> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Diff 37: Human Accept After ledger.spec.ts Regression Fix

## Context

Diff #37 (cap 3→2 reducer change touching `settle.go`/`autoland.go`) was blocked with `auto_land_blocked{verify_failed}`, and the user asked whether a manual accept was required. Answer: yes — terminal blocked state, panel full-vote gate no longer reachable, human accept is the unconditional escape hatch.

## Key Decisions

- **Manual accept required.** `≠panel_infra` blocked states are TERMINAL for diffs (autoland.go:150); the daemon never retries. Because #37 touches `protectedGateFiles` (`settle.go`/`autoland.go`), majority-panel attestation was revoked (2026-08-22 security reduction); auto-land needs a unanimous panel vote. Human accept (`TestHumanAcceptGateSourceAllowed`) bypasses this.
- **Gate-reported `pipeline-chip:84` failure was a timing flake** — passed in isolation and on spec rerun. Not acted on.
- **Patch file rewritten before accept.** `.odo/diffs/6a8a520e-*.diff` was regenerated per drain semantics (`git add -A && git diff --cached HEAD`) so the landed bytes include the fix. Sole delta vs. the stale patch = the ledger.spec.ts hunk (+68 lines). Safe because the diff never reached the panel — no `patch_sha16` attestation binding was broken.
- **`.odo/diffs/` is an append-only archive**, not a live queue (77 patches retained, accepted ones included). No queue-clearing action needed after accept.

## Code Changes

Root cause: #37 cut cap 3→2 and removed one revise round from fixtures (ledger rows 11→10, blocked re-pointed to diff 10) but missed coupled expectations in `gui/e2e/ledger.spec.ts`. Fixed five spots in the #37 worktree (repo-wide audit found no other coupling):

- Row count expectation → 10
- `Revise round 3` → `Revise round 2`
- "pre-W5 human accept" row index nth(4) → nth(3) (one fewer revise round shifts ordering)
- Badge counts: auto×7, risk-clean×5

## Verification

- `cd gui && tsc + playwright`: **117/117 green** (ledger.spec.ts previously 2 stable reds; main baseline was 4/4 green).
- `go build/vet/test ./...`: all green, including ipc cap tests (478s).
- Post-accept: #37 landed on main as `1203d93` (9 files, ledger.spec.ts fix included). Journal accept receipt shows disjoint-refresh re-anchored the 2 wiki-only commits (`954ff22 → a2de5b8`), `risk_class=[none]`. `go build ./...` on main HEAD passes.
- Cleanup: accidental daemon skeleton (`.odo/` from a bare `odo` run in the worktree) removed; `/tmp/diff37-regen.diff` deleted; worktree clean. Main daemon (PID 24652) unaffected.

## Open loops

- Untracked `package-lock.json` in the main checkout — user to delete or keep (was not swept into the commit).
- Daemon binary is stale again (HEAD advanced past it; staleness warning reappeared). Restart timing is the user's call since it kills in-flight sessions.