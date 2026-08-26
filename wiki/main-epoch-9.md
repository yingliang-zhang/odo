> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Odo Workstream Review — M20 Auto-Land & UI Switch-Cache

## Status
All agent-side blockers cleared. Four pending diffs resolve into two stale/revised pairs; only GUI acceptance by the user remains.

| Diff | Workstream | Content | Verdict |
|---|---|---|---|
| #23 | main | M20 auto-land initial version | Reject — superseded by #24 |
| #24 | main | #23 + settle race barrier + two stale-test fixes | Accept — full suite green |
| #21 | UI | Switch-lag fix initial version | Reject — superseded by #22 (auto-revise round 1) |
| #22 | UI | switch-cache module + server-side support | Accept — after #24 lands |

## Key decisions
- **Landing order: #24 before #22.** Both heavily modify `internal/ipc/server.go` (#24 +232 lines, #22 +145 lines); the later one must rebase and re-verify. #24 carries the most complete evidence, so it lands first.
- **Two test failures diagnosed as stale tests, not production regressions:**
  - `TestGetSettings` — M20 deliberately flipped the `auto_apply` default to `main` (fail-closed direction, justified in `settings.go` comment); `server_test.go` still expected `off` in both legs.
  - `TestPhantomDiffVerdictBlocksAutoLand` — M20 added an arming gate: prefs lacking a `review:` line leave the pipeline unarmed → silent exit with zero journal. The test's prefs had only `auto_apply: main`, so the verdict gate was never reached.

## Code changes
- Fixed both tests in the #24 worktree (`6a86c0b1`):
  - `TestGetSettings`: updated expected default to `auto_apply: main` in both legs.
  - `TestPhantomDiffVerdictBlocksAutoLand`: added the `review:` line to prefs so the M20 arming gate arms and the verdict gate is exercised.
- Both tests pass after the fix; #23's copy (`6a86b8a1`) differs from #24 only by these three fixes — no independent value.

## Evidence
- #24 full suite green across 4 packages (~7 min): `adapter` / `git` / `ipc` (420s) / root — exit 0.
- #22: UI vitest 9 files / 109 cases green, including new `switch_cache.test.ts` (12 cases); `go build`/`vet` clean. #21's 97 vitest cases also green, but its functionality is subsumed by #22.
- Prior session's background full-ipc run result was lost to the session interruption; re-verified this session.

## Open loops
- User to accept **#24** in the GUI (main workstream).
- User to reject **#23** and **#21** (superseded drafts).
- After #24 lands: user notifies agent → agent rebases **#22** onto new HEAD, reruns vitest + ipc-related tests, then user accepts #22.
- Awaiting user decision: whether to run the Playwright e2e spec (`switch-cache.spec.ts`) in a real browser before landing #22.