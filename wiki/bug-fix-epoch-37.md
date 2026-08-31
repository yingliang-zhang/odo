> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Diff #119 repair: fresh candidate demoted `shadow → dropped` in `TestDistillStagesCandidateEndToEnd`

## Context
- W5 diff #119 (9 files, +2338/−28: learning_rollback / measure / stages / lint + tests) failed the auto-land VERIFY gate in a fresh worktree cut from HEAD (`63bdaa1`, W4 on main): `learning_lifecycle_test.go:476: stage = "dropped", want shadow` (log: `.odo/verify/diff-119-1788156428856024000.log`).
- Root lead from the journal trail: the local full suite ran green (~13:56, `ok internal/ipc 517.143s`), but the diff was then re-applied and a conflict in `learning_lifecycle_test.go` (W5 hunks vs W4-restore base) was resolved *after* the suite finished — plus three post-resolution edits fixing the #118 panel finding (the decorated `learning_frozen` fold), all pre-seq-17381. The staged tree was therefore never the tree the suite validated.
- Task constraints: `internal/ipc/**` only, `settle.go` untouched, no supply-chain, leave worktree dirty, report focus + full gate tails.

## Key decisions
1. **Reproduce on the staged tree first.** Staged tree in run worktree `6a9514e5` is byte-stat identical to the #119 archive (9 files, +2338/−28); the focus test fails there, so the fallback (archived patch → scratch worktree cut from HEAD) was unnecessary.
2. **Failure semantics:** the e2e reads the *last* `learning_stage.to` transition; verify saw a later `dropped` transition, emitted by `learning_stages.go:213` (`shadow → dropped / shadow_failed` when the replay verdict fails).
3. **Nondeterminism ruled out where possible:** replay gates = lint + security + replay; input assembly is fully sorted (deterministic per content), excluding SQL ordering-tie leaks.
4. **`created_at` provenance:** diffs are SQLite-clocked and formats match, so the two-diff model cannot produce `outcomes=0` under any second placement → a **third diff row is required**; the redrive/recover path ("loop-owned diffs may re-fire") is the only injector, and it only fires under load.
5. **Terminal scan:** panel/vision terminals are filtered, but the seed terminal is plain `AgentDone` — so a re-fired diff's terminal is not filtered and skews the replay verdict/outcome attribution.
6. **Fix the code, not the test.** The W4 checkpoint contract (fresh, not-aged candidate ⇒ stays `shadow` ⇒ continue) is kept; W5 e2e expectations unchanged.

## Code changes (this repair session, `internal/ipc`)
- Fix targeting the under-load re-fire of loop-owned diffs (the third diff row that corrupts the replay verdict and demotes the age-0 candidate from `shadow` to `dropped`).
- Refined after stress-dump analysis: added a new call, corrected its declaration order (`dir` was declared after the new call), and added a helper function.
- Added a 100× stress harness (two hub-managed load-generator jobs) to exercise the load-sensitive path; rewritten with in-band `cd` after discovering the harness `bash` `cwd` parameter never lands (the shell stays in the session worktree) — use in-band `cd` instead.

## Verification
- Reproduced the failure on the staged tree: `go test ./internal/ipc/ -run 'TestDistillStagesCandidateEndToEnd'`.
- Stress dump pulled and analyzed; fix applied on that evidence.
- Load generator (two 100× jobs): **green**.
- Full suite launched via `nohup` after the final `git add` — **still draining** when the session ended.

## Open loops
- Full-suite gate tails pending: the suite was still draining at session end; it must validate the exact staged bytes.
- Focus gate tail (`go test ./internal/ipc/ -run 'Learning' -count=1`) on the final staged bytes was not explicitly reported.
- Explicit confirmation from the stress dumps of the third-diff-row / redrive mechanism (the fix's premise) was never stated in-session — the fix rests on it, and the re-run verify gate is the ultimate arbiter.
- Re-attempt auto-land of the repaired #119 once the full suite is green; confirm nothing was staged after the suite started, worktree left dirty, and `settle.go` untouched.