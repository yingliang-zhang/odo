> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Diff #33/#34 Disposal and Auto-Land Panel Rejection

## Key Decisions

- **#33 rejected as redundant.** Diff #34 is a strict superset of #33 (15 shared files, 0 unique to #33, 18 more in #34). #33 (P1 #5–#9 batch) had sat `pending` since the previous epoch after a `verify_failed`, never disposed — it was not "reappearing", it was never handled. User rejected it via GUI; socket query re-confirmed terminal state.
- **#34 re-triggered via daemon restart.** `panel_infra` is the only non-terminal blocked reason in `pipelineTerminalDiffIDs`; its designed resolution is the restart re-fire. A self-daemonizing watcher (pattern from `.odo/restart_watcher.py`) sent SIGTERM ~25 s after arming, the GUI respawn grabbed the socket (harmless, flock single-instance), and `recover-pending-diffs` re-ran #34 through verify→panel.
- **Restart proceeded only after token refresh.** Prior session blocked at panel because both review models (`t9s/kimi-k3@sudo`, `glm-5.2@sudo`) received HTTP 401 (`SSO or API token required`) from `https://coding.sudoai.cc/anthropic`. Pipeline fail-closed behavior (infra failure ≠ verdict → block, not pass) confirmed correct.
- **#34 landed nowhere — auto-rejected by panel.** Verify passed (2 s @ 06:05:09 UTC); panel completed 06:06:47 with `panel_mixed`: kimi-k3 accept, glm-5.2 accept, deepseek-v4-flash reject → auto-reject. Mechanical classifier tagged `risk_class:["destructive"]`.

## Panel Rejection Analysis

- **Reject was driven by a false "objective mismatch".** The review prompt anchored the objective to the session's last user message ("coding.sudoai.cc 应该可以访问了") rather than to the diff's actual provenance (P1 #10–#14 batch from the prior session).
- kimi-k3 and glm-5.2 both noticed the mismatch but accepted after auditing: all 4 protected gate files were strengthening or narrowing changes. glm-5.2 raised 3 narrow concerns (e.g. scratch-HOME exporting GOPATH).
- deepseek-v4-flash rejected with the unrelated-objective reason first, plus 3 technical objections: single-judge check position, GO cache mounted into sandbox, fanout exceeding cap.

## Code Change State

- **P1 #10–#14 code exists only in `.odo/diffs/6a89261c-d0af402d09cc.diff`** (202 KB, base commit ba68fb5). Worktrees are clean; the batch is not committed on any branch.
- The batch fixes real defects: shared client, verify sandbox, recover-pending-diffs excluding loop seed, etc.
- Prior session status before the block: build/vet green, focused tests green, `TestPanelContextBlock` panic (carried in via #33 content) fixed; the daemon's own auto-land verify run had exercised #34 once.

## Open loops

- **Land #34 manually?** `git apply .odo/diffs/6a89261c-d0af402d09cc.diff` onto ba68fb5 and commit — awaiting user decision; optionally audit deepseek's 3 technical objections (single-judge check, GO cache in sandbox, fanout cap) first.
- **Or abandon the batch** — accepting that the real defects it fixes remain open.
- **Systemic bug unfixed**: review prompt derives the objective anchor from the session's last user message instead of diff provenance — will recur on any resumed/re-fired review and should be fixed in the prompt construction.