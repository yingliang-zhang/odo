> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# odo: Verify Advisory Gate — Auto-Revise Rounds 1–2

## Context
Two rounds of panel findings against the pending autoland "verify advisory" diff (base `397ca3c`, diff #28). The pending diff was not materialized in the worktree; each round rebuilt the revised implementation there. Goal: when the verify gate blocks autoland because no usable `.odo-verify` command line is committed, surface a one-time advisory per run boot — without false suppression.

## Round 1 findings & resolutions

| Finding | Fix | Evidence |
|---|---|---|
| Uncommitted `.odo-verify` treated as configured, suppressing advisory | `projectVerifyConfigured` checks HEAD commit state via `git.CapturePatchBaseline` (existing `ls-tree HEAD` probe); run worktrees only materialize tracked files, aligning with gate semantics; any uncertainty (non-repo, read failure) fails open to advisory | `TestProjectVerifyConfigured` untracked/staged-only → unconfigured; `TestVerifyAdvisoryFiresWhenConfigUncommitted` |
| Journal failure swallowed the boot's only reminder | `journalRunAdvisory` returns `error` (all 5 existing Go statement callers compatible); on advisory-path failure, `verifyAdvised.Delete` releases the debounce so the next blocked diff retries | `TestVerifyAdvisoryReleasedOnJournalFailure` (FK injection via non-existent conversationID `1<<40`) |
| `npm test` could be fake advice | `npmHasTestScript` parses package.json; `npm test` suggested only when `scripts.test` is non-empty, else generic fallback wording | `TestVerifySetupAdviceToolchainHint` |

Test infra lesson: an extra commit in the fixture advanced HEAD past `d.BaseSHA`, and the `revise_ambiguous` freshness ladder fires before the verify gate — fixture must re-read HEAD as BaseSHA after committing.

## Round 2 findings & resolutions

| Finding | Fix | Evidence |
|---|---|---|
| "Configured" judged from disk copy, not HEAD | New `git.ShowHEADFile` (`git show HEAD:.odo-verify`) reads committed content; stub-HEAD-plus-dirty-disk, untracked, staged-only all classify `verifyConfigNone` → advisory fires | `TestShowHEADFile`; `TestVerifyAdvisoryFiresWhenConfigUncommitted/tracked_dirty_past_a_stub_HEAD` |
| Scoped-only config + out-of-scope diff suppressed to zero signal | Three-state semantics `None`/`Partial`/`Ready`; shared `parseVerifyFile` extracted from `verifyCommands` (single parser, no drift); out-of-scope diff → `Partial` → scope-flavored advisory listing committed globs and suggesting a fallback line or covering glob; covered diff → `Ready` → suppressed | `TestVerifyAdvisoryScopedConfigCoverage` (both directions); glob-truncation case |
| Debounce failure-release race under concurrency dropped reminders | `verifyAdviseMu` serializes claim+journal+release: journal first inside lock, key occupied only on success; failure never occupies key; lock ordering guarantees release precedes retry | `TestVerifyAdvisoryConcurrentSingleRow`: 1 FK-broken + 7 normal concurrent callers → exactly 1 row, `-race` clean |

Wording corrected: "no usable command line is committed in .odo-verify" (old text lied in comment-only-stub cases where a file is committed but contains no command).

## Final change footprint
- `internal/git/git.go`, `git_test.go` — new `ShowHEADFile`
- `internal/ipc/verify_advisory.go` — new advisory/gate hook logic
- `internal/ipc/verify_advisory_test.go` — new, 9 tests incl. subcases, `-race`
- `internal/ipc/autoland.go` — `parseVerifyFile` extraction; hook passes `verifyPaths` (from `git.PatchPaths(d.PathOnDisk)`)
- `internal/ipc/runverdict.go` — `journalRunAdvisory` returns `error`
- `internal/ipc/server.go` — `verifyAdviseMu` + `verifyAdvised` map (nil lazy init)

## Verification
- `go build ./...` + `go vet ./...` clean; `gofmt` clean
- Target tests 9/9 with `-race`; adjacent regressions (`TestAutoLand*`, `TestParked*`, `TestSilentRun*`) pass
- Full `go test ./... -count=1`: 446s (round 1) and 450s (round 2), all green, 0 FAIL — same command the landing gate runs; diff doesn't touch `gui/**`, so only the fallback line triggers

## Open loops
None.