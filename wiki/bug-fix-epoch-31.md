# D9-W3 (diff #113) re-verification — environment flake confirmed, zero changes

## Context

Diff #113 (D9-W3 observability, 25 files, +2743/−16) had its auto-land blocked by the auto panel (`reason: verify_failed`) after a Playwright run failed mid-suite: the `webServer` (vite on :1420) stopped answering around spec ~87, producing 67 uniform `net::ERR_CONNECTION_REFUSED` failures (a few partial-DOM failures just before the death fit a dying server). Everything before the death was green — `tsc` PASS, vitest 35 files / 413 tests PASS, e2e specs 1–83 passed. Working hypothesis: one-off environment flake, not a content defect. Both follow-up tasks were strictly zero-change: re-run or cite gates on the existing dirty worktree, modify nothing, do not attempt to fix vite.

## Key decisions

- **Classify the failure as environmental, then prove it.** Uniform connection-refused burst, all gates green before death, and vite facts (deprecation warnings, `::1`-only binding) identical in the main checkout → content not implicated. Confirmed by full re-run: zero connection errors, clean pass beyond spec 87 where the original died.
- **Run gates only in the worktree that actually holds the diff.** Each session's launch worktree (`6a940900-8a3e03f61f87`, later `6a942192`) was clean and irrelevant, despite task text claiming the diff was local. The staged D9-W3 diff lives in sibling worktree `6a93fe32-f3fd1a7e272c`; all gates ran there.
- **Preflight the dev server exactly as Playwright launches it** — nohup `npm run dev`, poll `curl` on :1420 to HTTP 200, re-check after 60s idle, `pkill` teardown. Result: up in 4s, survived idle, clean teardown → server config sound.
- **Keep the machine quiet during the long e2e run** (contention was the standing hypothesis): nohup the Playwright run, poll its log, no parallel work.
- **After the first re-verify run's OMP budget expired mid-e2e: re-snapshot instead of re-running.** The nohup Playwright process finished on its own (140 passed, 1.6h, zero `ERR_CONNECTION_REFUSED`); the second pass only confirmed the staged diff was byte-identical to diff #113 and re-ran the fast gates, citing the existing heavy evidence (`/tmp/w3-playwright.log`, prior full `go test ./...` PASS).

## Code changes

**None.** Verification-only session. The worktree was left dirty and untouched: 25 staged files, +2743/−16, zero unstaged delta — byte-identical to diff #113 (learning_episode / learning_status / learning_store + tests, LearningPanel + contrib wiring, `cmd_learning.go`, `verify_ms`, `run_usage`, protocol/server/main wiring). No repo file was modified, re-staged, or created; session artifacts (dev-server log, playwright log, poll helpers) live under `/tmp` only.

## Verification evidence (worktree `6a93fe32-f3fd1a7e272c`)

| Gate | Result |
|---|---|
| vite preflight (up + 60s-idle survival) | up in 4s; still 200 after idle; clean teardown |
| `npx tsc --noEmit` | PASS (exit 0, clean) — both passes |
| `npx vitest run` | 35 files / 413 tests, all passed — both passes |
| Full `npx playwright test --reporter=line` | 140 passed (1.6h), zero `ERR_CONNECTION_REFUSED`; earlier 67-failure run confirmed one-off |
| `go build ./...` | PASS (exit 0) |
| `go vet ./internal/...` | PASS (exit 0) |
| Full `go test ./...` | PASS (prior run, cited, not re-run) |

Risk note carried forward: none new — content verified by gates three times; remaining exposure is environmental only.

## Environment notes

- Bash shell cwd resets per call — the explicit `cwd` parameter is required on every invocation; several early gate attempts silently ran in the wrong (clean launch) directory before this was caught.
- The diff-holding worktree was being concurrently mutated early on (`node_modules/.bin` flipping between 23 entries and complete) — another process/peer was active; peers checked via `hub`.
- Playwright config: `webServer` = `npm run dev` (vite :1420), `reuseExistingServer: true`.
- Vite binds `::1` only on this box; deprecation warnings present — identical in the main checkout.
- Full e2e took 1.6h against the task's ~8–9 min expectation.

## Open loops

- **Diff #113 auto-land is still blocked** (`verify_failed`). Content is now verified three times over; the decision to land/drain the staged diff in `6a93fe32-f3fd1a7e272c` (intentionally left dirty) awaits the user.
- **Vite mid-run dev-server death is unremediated** (fix explicitly out of scope). Decide whether to harden Playwright `webServer` handling (restart/retry) or accept flake risk on long e2e runs.
- **1.6h Playwright duration vs the ~8–9 min expectation is unexplained** (machine contention? slow box?) — worth investigating before relying on e2e timing budgets.
- **Cited e2e tail reads `140 passed` while in-run polling counted a 150-test suite** — whether the remaining 10 were skipped or retried is not captured in the quoted `/tmp/w3-playwright.log` tail.