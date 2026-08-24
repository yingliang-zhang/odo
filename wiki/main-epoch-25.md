# #39 Block Investigation: Latent Test Defect on Main, Fixed

## Summary

#39 was blocked with `auto_land_blocked{verify_failed}` (a terminal state — no replay on restart, autoland.go:146-156). Root cause was **not** #39's changes but a latent test defect on main, first triggered because #39 was the first diff to actually run through verify since the M20 arming gate landed (via cherry-pick 4345f24).

## Root Cause

- The sole failing test in faithful reproduction: `TestAutoLandStartedRowsAbsentBeforeSpend` — `blocked reasons = [], want [supply_chain_path]`.
- The test had neither `t.Setenv("HOME")` isolation nor arming. `autoLand`'s first gate is the M20 arming gate, which reads `review:` from prefs.md in the process HOME.
- Verify has used a scratch HOME since P1#11 (credential isolation) → no prefs → unarmed → silent zero-row exit → FAIL. Machines/sessions with a review configured → armed → PASS. Environmental lie, environment-dependent.
- Audit of the file's 24 autoLand pipeline cases: 23 had isolation + arming; this one was the only exception (the convention is documented at file L352 — this case slipped through).

## Code Changes

Fix diff in `internal/ipc/autoland_test.go` (**+6 lines**):
- Added HOME isolation + `review:` arming to `TestAutoLandStartedRowsAbsentBeforeSpend`; zero semantic change.

Verification:
- Pre-fix faithful reproduction (byte-identical worktree + scratch HOME + filtered env): exactly this one FAIL across the full suite.
- Post-fix: the test and the entire TestAutoLand group green under scratch HOME; gofmt/vet clean.
- No file overlap with #39, so base-freshness adjudication handles main moving forward.

## Decisions & Next Steps

- After the fix diff lands, **Accept #39 once** — the only unlock path (blocked is terminal; bytes were already panel-reviewed). Re-running is equivalent but costlier (burns a run + panel, zero information gain).
- After both diffs land, restart the daemon (write-then-kill, same protocol as 10:25) so #39's registration-window rejection takes effect.
- Normal flow resumes without human intervention: run ends → drain registers → verify (now green) → majority panel → auto-land.

## Observability Gap (reported, unfix­ed)

`capDetail` keeps the first 4KB of an 8KB tail, but `go test` failure diagnostics (`--- FAIL` lines) live at the tail end — so `verify_failed` blocked rows never name the failing test; only reproduction reveals it. Suggested follow-up: extract `--- FAIL` lines into detail (or keep the tail's end) in a separate diff.

## Open loops

- P2: remaining 19 files must strip the `wiki/topics/gui-visibility.md` hunk then re-register — must wait until #39 is active, else repeat the #38 deadlock.
- "Stuck" definition content routing via distill/`commitWiki`.
- Follow-up diff: make `capDetail`/verify detail expose `--- FAIL` lines for diagnosable `verify_failed` rows.