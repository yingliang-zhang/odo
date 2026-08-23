> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Odo Session: Recovering Orphaned Commits and Major-Vote Policy Patch

## Problem discovered

Epoch-18's two "landed" submissions had never reached main. `#34` (`a5c98ca`, 8 P1 fixes across autoland/learner/ledger/cmd_journal) and the anchor fix `abca6f1` (schema v3, `goal` column on `diffs`) were committed on the detached HEAD of a since-deleted session worktree, leaving them orphaned. Main HEAD remained `ba68fb5` (#32). Consequences:

- Anchor bug still live: `diffs` table lacked `goal`; delayed/re-fired reviews could bind the wrong objective.
- Ledger correction at seq 8017 falsely claimed "manually landed in main: a5c98ca"; the correction itself needed correction.
- #35 (majority-vote settle policy) was auto-rejected by the panel (`panel_mixed`); one dissent held: `settle.go` header said truncated legs count as needs_fixes, but the vote-count loop switched only on `r.Verdict`.
- Full test suite had an unconfirmed 472s FAIL; no complete green run.

User approved a four-step plan, executed in-session.

## Changes landed

| Step | Result | Evidence |
|---|---|---|
| Cherry-pick orphans into main | `ba68fb5 → 4345f24 → 954ff22` | Clean cherry-pick in worktree + `--ff-only` in primary repo; `git diff abca6f1 HEAD` empty; wiki uncommitted changes untouched (zero overlap) |
| #35 rebuilt + truncated-leg fix | `git apply --3way`, 4 files clean on new main | Cap-count loop gained explicit truncated→needs_fixes abstention |
| Second journal correction | seq 8470, `corrects_seq: 8017` | Declares 8017's premise invalid; `4345f24`/`954ff22` are authoritative; #33/#34/#35 rejected statuses left as-is per convention |
| Full test suite | `go test ./...` EXIT=0, 509.65s | 7 packages green; gofmt debt limited to the same two known files; vet clean |

## Additional findings and fixes

- Dissent's factual basis partially dissolved but fix kept: `reviewVerdict` already forces truncated→needs_fixes at construction (`server.go:3251`), so the cap loop never counted such legs. The explicit `continue` was added anyway as local self-proof against future producer regressions, with a comment explaining the invariant.
- Root cause of the 472s FAIL: `waitSettle` wait conditions vs. subsequent assertions exposed an evidence→action→supersede ordering window (producer writes sequentially in one goroutine; the test polls). Per the audit-all-similar-sites rule, 5 race points were fixed: `TestSettleCapMajorityRejects` (1 compound condition), 3 suspension waits, and the auto-land accept supersede window. Race battery passed 5 consecutive runs.
- New test `TestReviewVerdictTruncationForcesNeedsFixes` pins the construction-side downgrading contract the cap loop's abstention semantics rely on.
- Removed 4 stale comments/log lines referencing the retired "majority-accept valve" (`protocol.go`, `server.go`, `loop.go`, `settle.go`).

## Current state

- Worktree holds exactly the 8-file policy patch uncommitted; HEAD = `954ff22` = main.
- `drainRun` on session exit will extract the worktree-vs-HEAD diff → new diff → verify + panel fires automatically.
- New daemon binary built at `odo` (= `954ff22`, excluding unreviewed policy); the running daemon (PID 6068) is still the Aug 20 binary.

## Open loops

- User must restart the daemon with the newly built binary after this session ends; otherwise a mixed panel verdict triggers instant auto-reject under the old (abolished) rules. If already mis-rejected, resubmit via review_diff after restart (patch files are on disk).
- Patch touches `autoland.go`/`settle.go` (protectedGateFiles): even a unanimous panel pass requires the user's manual Accept before landing.
- Wiki distill backlog (uncommitted changes) and untracked `package-lock.json` in the primary repo remain undisposed — outside approved scope.
- Known gofmt debt in `loop_audit.go`/`server_test.go` retained by prior decision.
- `tail-leg starvation → false panel_infra` behavior retained by prior decision (fail-closed; no instances observed).