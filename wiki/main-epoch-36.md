> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Auto-Land Epoch: Diff #48 Recovery Accept + Pipeline Hardening (#49)

## Context

Diff #48 was blocked by the auto-land gate (`auto_land_blocked`, reason `verify_failed`). The block turned out to be a **transient misjudgment**, not a real regression; the user accepted it, then ordered the 3 known pipeline-level legacy defects fixed in the same pass. Those fixes constitute diff #49.

## Key Decisions

- **Accept #48 manually.** Byte-level evidence chain showed the verify failure was spurious:
  - Archive identity `sha16=1a60b68c9fc68f15` consistent across started/blocked journal rows and disk recompute — archive never swapped.
  - Payload = #47 archive + exactly 1 assertion-line fix (`refusing to write through symlink` → `symlinked component` in `replay_test.go:357`).
  - 27 dirty worktree paths matched patch paths exactly.
  - Same bytes re-run: `go test ./internal/ipc -count=1` → exit 0, 532 s, zero FAIL (gate had FAILED at 513 s); other gate legs (build+vet+test, tsc, playwright 119/119) had already passed on identical bytes in epoch-35.
  - Followed the #42 manual-override precedent: same-bytes reproduction green → re-run has no information gain.
  - Root cause of the flake: gate verify ran concurrently with idle distill (model calls + wiki commits); `internal/ipc` FAIL tail was log-flood from sweeper/todo test daemons — same load-sensitive class as epoch-34.
- **Fix the 3 legacy pipeline defects in one payload** rather than separate diffs: verify-log persistence, reject-path worktree rescue, ContextPanel e2e flake.
- **Reject-path rescue = snapshot, not lifecycle change.** Before `retireRunForDiff`/`retireRun` removes the worktree (accept and reject share this path), snapshot the current full delta; only if it differs from the archived patch, write `.odo/diffs/<run>-rescue.diff`. The reviewed patch bytes are never rewritten, and the sweeper exemption for `.odo/diffs` guarantees survival. Worktree retirement stays as-is.
- **Verify output persisted to disk, journal keeps a pointer.** Full gate output → `.odo/verify/<label>-<ts>.log` (1 MB tail cap per file, prune to newest 32); journal still stores the 4 KB `capDetail` tail, with the `[full verify output: ...]` path appended **after** capping so it can never be truncated away. Success path also persisted (new `verify_log` field on moa row).
- **e2e flake fix = `expect.poll`.** Root cause: `scrollIntoView`/`scrollBy` run in post-commit effects; single-shot assertions race rAF. Contract is "eventually stably visible", so assertions became polling retries.
- **No repo-wide gofmt re-format.** New gofmt flags blank-line-after-top-level-decl across untouched files (`git.go`, `server.go`); project gate is build+vet only, so mass churn was refused.

## Code Changes (7 files, +436/−53; payload of #49)

| Defect | Change | Files |
|---|---|---|
| capDetail 4 KB truncation eats `--- FAIL:` line (blind reproductions in #47 and #48) | `runVerify`/gate persist full output via `writeVerifyLog`+prune, `keepTail`; label threaded through | `autoland.go`, `loop_run.go` |
| `reject` deletes candidate worktree; sole survivor was a stale `.diff` | `handleDiffAction` + `rescueResolvedWorktree` snapshot pre-retirement; rescue receipt fields (`rescue`, `rescue_path`, `rescue_sha16`) appended to existing resolution journal row; sweeper exemption comment | `server.go`, `sweeper.go` |
| ContextPanel strip-scroll e2e flake (load-sensitive) | rAF-racing single-shot assertions → `expect.poll` | `gui/e2e/context-panel-tabs.spec.ts` |

Prior payload (#48, landed as `1b5e2ed odo: accept diff #48`, 27 files +954/−175): symlink guard, keep-alive refresh, `/preview` pin to 1.62.1, hidden-panel memo, replay assertion fix.

## Verification

- `go build ./... && go vet ./...` green; `cd gui && npx tsc --noEmit` green.
- New contract tests all pass: `TestRunVerifyGatePersistsLog` (2/2), `TestWriteVerifyLogPrune`, `TestKeepTail`, `TestRejectArchivesWorktreeRescue`, `TestRejectMatchesArchivedSkipsRescue` (also confirms reviewed-patch bytes untouched).
- Regression suite: `TestReviewOfOlderDiffRetiresItsOwnRun`, `TestReviewDuringLiveRunKeepsLiveRun`, all existing runVerify/sandbox tests pass.
- Full `go test ./internal/ipc -count=1`: ok, 539 s.
- Playwright `context-panel-tabs.spec.ts` ×5: **10/10 PASS**, run concurrently with the 539 s Go suite — i.e., under the historical flake's exact load conditions.

## Environment Hiccup (unrelated to changes)

First e2e run died 10/10 in 4.3 s: `node_modules` symlink was accidentally created at the worktree root, so playwright ran config-less (no `baseURL` → `invalid URL`). Fixed by linking into `gui/` (same as gate's `provisionVerifyDeps`); path is gitignored and never entered the diff.

## Open loops

- **#49 pending review**: end-of-turn drain snapshots the 7 files → diff #49; verify gates + panel review (~12 min) must pass, then user clicks Accept to land on main. First run to exercise the new `verify_log` persistence on main.
- **Repo-wide gofmt style drift** acknowledged but deliberately deferred (new gofmt vs old-format tree; reformat would touch untouched files).