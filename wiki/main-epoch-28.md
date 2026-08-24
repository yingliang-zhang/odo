# Third-party review adjudication: 4 findings verified (3 confirmed, 1 misattributed) and all fixed

## Context

A third-party model reviewed the odo repo HEAD and reported 4 issues. Verification against code and experiments confirmed 3 as real; the 4th (GUI test failure) reproduced but was misattributed — it is a test defect, not an app bug. All four were then fixed in one pass (7 files, +431/−19), left in the worktree for the auto-land pipeline.

## Findings and fixes

### P0 — Accept rollback destroys user's uncommitted changes (CONFIRMED)

- **Root cause**: `CapturePatchBaseline` (git.go) records only existence booleans (`inHEAD`, `onDisk`), no content. `RollbackPatchApply` runs `git reset -q HEAD -- <paths>` + `git checkout -- <paths>`. Neither the accept path nor the stale-base refresh path (`checkAndRefreshBase`) had a dirty-path precheck.
- **Reproduction** (temp repo, both variants): `git apply --3way` atomically refuses on dirty paths (writes nothing), yet rollback still wiped the user's unstaged *and* staged content. A non-failing variant also existed: user edits in a different region get clean-3way-merged and swept into the `odo: accept diff #N` commit (pollution).
- **Fix**: new `git.DirtyPaths` helper (with `HasPathChanges` as thin wrapper); both apply sites in `handleDiffAction`'s accept branch refuse when patch-own paths carry staged/unstaged/untracked content. Refusal names paths, keeps diff pending, allows clean-and-retry; stale-base refusal journals `refresh_attempted{dirty_refusal}`.

### P1 — `retireRun` retires the wrong run's worktree (CONFIRMED)

- **Root cause**: `retireRunForDiff` passed the diff's own worktree only as `fallbackWT`; `retireRun` preferred the `byConv` binding. With two sequential runs in one conversation, reviewing older diff A closed/deleted newer run 2's worktree + session; could also kill an in-progress auto-land verify (runs in the diff's own worktree), causing spurious `verify_failed` auto-reject.
- **Fix**: retire target selected by matching `worktreePath` against `s.runs` (the diff's own run); `byConv` used only when no worktree match; binding reaped only when it points at the closed run.

### P2 — Large-file preview reads whole file into memory (CONFIRMED)

- `handleReadFile`'s >512 KiB branch called `os.ReadFile` then sliced, contradicting its own comment. Fixed with `os.Open` + `io.LimitReader(cap+1)` streaming read.

### GUI test failure — reproduced, attribution REJECTED (test defect)

- `switch-cache.spec.ts:64` failed deterministically 5/5, but instrumentation revealed: arming `fail:true` raced the mock's fixed 50 ms invoke delay; the *previous* main switch failed in-flight, rollback correctly restored the pre-flip view, and the later feat click was a correct no-op guard hit.
- **Fix**: dev fixtures gained a per-workstream `bootstrapLandings` counter (proves fail/delay toggles were consulted); the spec waits for the main landing signal before arming failure. 10/10 with `--repeat-each=5` (was 5/5 fail).

## Key design decisions

- **Refusal NOT wrapped as `errBaseStale`** — wrapping would trigger auto-land's auto-revise loop, burning ~8.5 min of verify per round regenerating a patch that hits the same refusal; user dirt requires human triage, not another cycle. Auto-land side: log + diff pending + manual triage.
- **M20 identical-content rescue ordered before the refusal** — `ProbeAlreadyLanded` still runs first; unstaged identical edits land via bookkeeping, not killed by the dirty check.
- **Pinned semantics preserved**: `TestReviewDuringLiveRunKeepsLiveRun`, `TestAcceptDoesNotSweepMainCheckout`, no-diff drain's `byConv` reaping all pass unchanged.

## Files changed

- `internal/git/git.go` — `DirtyPaths`, wrapper
- `internal/ipc/server.go` — dirty-path guards ×2 + refusal constructor, `retireRun` rewrite, streaming preview read
- `gui/src/dev/fixtures.ts`, `gui/src/dev/mock-invoke.ts` — landing counter
- `gui/e2e/switch-cache.spec.ts` — wait-for-landing before arming fail
- Tests: `TestDirtyPaths`, `TestAcceptRefusesDirtyPatchPaths` (unstaged+staged variants + retry-after-clean), `TestAcceptRefreshRefusesDirtyPatchPaths` (stale path, refusal journal, no sentinel wrap), `TestReviewOfOlderDiffRetiresItsOwnRun` (three assertions all dead under old semantics), `TestReadFileSparsePreview` (4 GiB sparse file)

## Verification

- New targeted tests + adjacent pins all green.
- Full `internal/ipc` suite: 494 s, green; `go test ./...`: repo-wide green.
- GUI vitest 110/110; typecheck clean; switch-cache spec 10/10.
- Instrumentation probes (App.tsx logging, probe spec) added for diagnosis and fully reverted; worktree clean of scratch artifacts.

## Open loops

- Reviewer's claimed warm-effect cache pollution in `App.tsx:665-673` (render-closure `events` vs. mutable refs written into target conversation cache) exists in code but no pollution was observed under instrumentation; needs a dedicated repro before any code change is justified.
- Fix changes (7 files, +431/−19) were left uncommitted in the worktree, awaiting the run-end auto-land pipeline; confirm landing outcome.