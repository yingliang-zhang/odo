# #40 Accept Decision and Auto-Land Pipeline Repair

## Context

Review item #40 landed in terminal `auto_land_blocked{verify_failed}` (autoland.go:146-156: no replay, no re-review; Accept is the only unlock). It is a +6-line fix to `TestAutoLandStartedRowsAbsentBeforeSpend` (scratch HOME isolation + review-prefs write), the root-cause fix for #39's block. The fixing session had only run `-run` subsets, **never the full suite**, so the 04:31 verify still failed.

## Decisions

- **Accept #40**: a faithful verify reproduction (same byte set 2ca27a8+#40, scratch HOME + allowlist env, full suite, 499s) proved the failure was unrelated to #40:
  - `TestAutoLandStartedRowsAbsentBeforeSpend`: green (also green across an 8-way single-test env matrix) — the fix works.
  - `TestPlaywrightBrowsersDir`: the only failure, **deterministic**. runVerify exports `PLAYWRIGHT_BROWSERS_PATH` when the host has an ms-playwright cache; the test's first assertion ("uninstalled should be empty") never scrubbed the env var. Pre-existing environment-sensitive defect on main, unrelated to #40.
- The journal's blocked detail for #40 again lost the `--- FAIL` lines to `capDetail` head-trimming; the real failure set could only be recovered by reproduction. That the daemon's 04:41 failure contained the Playwright test is [INFERENCE] from the determinism.
- After accept, a follow-up fix diff was required, or the next diff would again burn ~8.5 minutes to the same `verify_failed` terminal state. Accepted and implemented immediately.

## Code changes (follow-up diff, left in workspace → auto-land pipeline)

| Fix | Location | Change |
|---|---|---|
| Playwright test determinism | autoland_test.go:2193 | `TestPlaywrightBrowsersDir` starts with `t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "")` |
| Telemetry race | autoland.go:1102 | `goToolchainCacheEnv`'s `go env` subprocess pinned to `GOTELEMETRYDIR=$TMPDIR/odo-go-telemetry` (passes through when explicitly configured). Same-class audit found `TestRunVerifyMountsGoToolchainCaches` itself runs bare `go env` with scratch HOME — pinned the same way. Eliminates the go-telemetry async-write race that once caused a `TestAutoLandVerifyNoEvidence/zero_evidence_blocks` TempDir cleanup flake |
| capDetail detail loss | autoland.go:1221 | head-trim → tail-bias: prefix `…[earlier truncated]\n`, rune-safe cut, 4KB cap — keeps `--- FAIL` lines alive in journal blocked details |

Supporting changes:
- `review_test.go:292` was the only test pinning the old head-trim contract (`HasSuffix "…[truncated]"`) — updated to the tail contract. All other detail assertions are short-string `Contains`, unaffected.
- New `TestCapDetailTailBias`: short-circuit, 4KB cap, FAIL-line survival, UTF-8/CJK boundary safety.

## Verification

- **A/B proof** for Fix 1: with `PLAYWRIGHT_BROWSERS_PATH` injected (exact verify-failure scenario) — before fix FAILs reproducing the verify output (`uninstalled browsers = ".../ms-playwright"`); after fix PASSes.
- Full `internal/ipc` package: 493s all green (same magnitude as the daemon's 499s verify).
- Full-repo build passes.
- Tool defect logged: a `bash` `cwd` parameter did not take effect, causing the first reproduction to run against the wrong tree; reported.

## Open loops

- Follow-up diff (three fixes above) is committed to the workspace and will enter the auto-land pipeline at run end; its verify is still pending — pipeline zero-human-operation is restored only after it lands.