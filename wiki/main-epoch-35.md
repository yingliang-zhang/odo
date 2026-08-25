> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Diff #47 Block: Stale Symlink-Guard Assertion — Rejected, Recovered, #48 Pending

## Root cause

`auto_land_blocked` on #47 with `verify_failed` was a **deterministic failure, not a load flake** — a same-bytes re-run reproduced the identical FAIL.

| Item | Fact |
|---|---|
| Failing test | `TestAgentsMDRefusesSymlinkWrite` (`internal/ipc/replay_test.go:357`) |
| Cause | Epoch-34 replaced the AGENTS.md special-case Lstat branch with the unified guard `guardProjectWritePath`; the rejection log changed from `"refusing to write through symlink"` to `"symlinked component …"`, but the test still asserted the old text — epoch-34 missed it |
| Guard behavior | Functionally correct — sentinel bytes untouched, no panic; both safety assertions passed in the original failing run. Only the log-text assertion was stale |
| Audit | Repo-wide grep: this was the only stale assertion site; the full gate run served as the audit |
| Diagnosis friction | The original gate's `--- FAIL` line was eaten by the capDetail 4KB head truncation; the failing test was located via a same-bytes re-run |

## Code changes

1. **In diff-47 worktree (patch sha `6a8d1143`)**: `internal/ipc/replay_test.go` — 1-line assertion change to `"symlinked component"`.
2. **After reject (see incident below)**: 26 files restored into the current worktree from `.odo/diffs/6a8d1143-01b47beabc9a.diff` (+953/−174), plus the re-applied 1-line assertion fix. All unstaged — exactly the intended #48 payload.

## Verification (post-fix, exact bytes now in worktree)

| Gate line | Result |
|---|---|
| `go build ./... && go vet ./... && go test ./...` | **exit 0** (525s; previously-hanging ipc package now green) |
| `cd gui && npx tsc --noEmit` | PASS |
| `npx playwright test` | **119/119** — closes the epoch-34 dangling e2e open loop |
| AGENTS test cluster | 3/3 PASS |

## Recover-after-reject incident

A planned unblock said: reject #47 (its archived `.diff` bytes contained the bad assertion — accepting would land the broken test in main), then let drain snapshot the surviving worktree into #48. **Rejecting #47 actually deleted its worktree** — the prior plan's assumption was wrong. The fix briefly existed only in the diff backup. Recovery: dry-check backup → clean apply (26 files) → re-apply assertion fix → affected test cluster passed immediately. Pipeline hazard: reject destroys the worktree; sole surviving artifact is the `.diff` file.

## Prior review findings (ab20b62 deep review) — all confirmed fixed

| Finding | Status | Evidence |
|---|---|---|
| P0 symlink-guard root bypass | ✅ Fixed (three-arg guard + canonical projectRoot anchor + equal guard on write side) | Guard test cluster passed with `-race` |
| P1 keep-alive panel stale data | ✅ Fixed (activation-triggered refetch + draft protection + `active` prop) | vitest 7/7 |
| P1 `/preview` unpinned remote code | ✅ Fixed (lockfile pins 1.62.1 + explicit setup phase + offline verification) | `ODO_PREVIEW_LIVE=1` live run passed |
| P2 hidden-panel streaming recompute | ✅ Fixed (memo key pinned to latest relevant event seq) | typecheck + build passed |
| #47 gate-blocking assertion | ✅ Fixed (assertion aligned to new guard text) | Re-run PASS after recovery |

## Open loops

- Generate **#48** and land it (human gate): send any message on the main workstream → drain snapshots the current worktree → verify (both lines already proven on identical bytes) → panel accept. Accepting lands the fixed bytes into main.
- Pipeline design question: reject deletes the candidate worktree, leaving the `.diff` backup as the only copy — decide whether rejects should preserve the worktree (or auto-restore) to avoid manual recovery like this one.