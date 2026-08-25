# Auto-distill goroutines joined into Server lifecycle (distillWG fix)

## Deployment of prior hardening batch

- User approved deployment; agent accepted diff #15 via unix socket → landed as `cf56e72 odo: accept diff #15` (10 files, +532/−28); worktree `6a85c365` recycled.
- Anomaly during validation: `go test -run` found no tests in the worktree; root cause was shell/cwd ambiguity, files on disk were correct — rerunning with explicit `cd` prefix passed both validation suites.
- Rebuilt binary, atomically replaced daemon binary (incl. bootstrap copy), built Odo.app. Daemon restarted mid-request (expected after binary swap); no reply recorded, in-flight request replayed.
- Transient `auto_land_blocked` (`revise_spawn_failed`) occurred earlier in the session; superseded by the successful accept.

## P1 fix: distill goroutines outside `Server.Wait()`

**Problem.** `auto.go:386` timer callback ran `go s.runAutoDistill(...)` with no WaitGroup; `Wait()` drained only `wg`/`curateWG`/`loopWG`/liveness. Fired distills kept writing journal/wiki/git after teardown closed the store. Reproduced on main `7e1bed8`: `TestAutoUrgentUpgradeSupersedesIdle -count=3` failed 2/3 with `TempDir RemoveAll cleanup: directory not empty` (urgent-upgrade `AfterFunc(0)` fires immediately; rig.stop's inline disarm can't find the already-claimed entry and closes the store under the running goroutine).

**Key decisions**

- Join, don't abort: fix is lifecycle membership (WaitGroup), not cancellation. In-flight send/steer/slash pre-note cancel semantics via `autoInFlight` unchanged; no context changes in `runAutoDistill`.
- Add-before-go under `s.mu` after the timer identity check — byte-copy of the `maybeAutoCurate`/`curateWG` pattern.
- `autoStopped` flag + `stopAutoDistill()` (mirrors `stopLiveness`): one subsystem-close path that bars all post-shutdown arms (backoff, supersession re-arms) and Stops pending timers; already-fired callbacks no-op via slot identity check.
- `Wait()` ordering: `recoverWG.Wait()` → `stopAutoDistill()` → `distillWG.Wait()`, before the caller's store teardown — a long distill completes against an OPEN store (the point of the fix).
- `recoverPendingDiffs` constructor spawn registered under new `recoverWG`; its internal `maybeAutoLand` fan-outs deliberately NOT joined (auto-land is restart-interruptible by design via `auto_land_started`/`refresh_attempted` breadcrumbs; joining = behavior change, separate pass).
- Race-safety argument: all `Add(1)`s serialize against map-clear/flag-set on `s.mu`, so a fire past the identity check is counted before `distillWG.Wait()` can return.

**Code changes**

| File | Change |
|---|---|
| `internal/ipc/server.go` | `distillWG`, `recoverWG`, `autoStopped` fields; `stopAutoDistill()`; `Wait()` drain order; constructor spawn under `recoverWG` |
| `internal/ipc/auto.go` | Timer callback: `distillWG.Add(1)` under `s.mu`, `defer Done()`; `armAutoLocked` quiet-returns when `autoStopped` |
| `internal/ipc/server_test.go` | `testRig.stop`: replaced M12 inline disarm loop with shared `stopAutoDistill()` + `distillWG.Wait()`/`recoverWG.Wait()` before `store.Close()` (rigs never stop `Serve`, so they bypass `Wait()`). Only rig touched; no test bodies changed |

Mid-task incident: an inline edit corrupted indentation in the `auto.go` callback block (inherited tab from edit marker); repaired via block rewrite; `gofmt`/build confirmed clean.

**Verification**

- `go build ./... && go vet ./internal/...` → OK
- `go test -race ./internal/ipc/ -run 'TestAutoUrgentUpgradeSupersedesIdle' -count=6` → `ok 5.881s` (6/6)
- `go test -race ./internal/ipc/ -run 'Auto' -count=2` → `ok 108.577s`
- Full `go test ./internal/...` → `ok` on adapter/git/ipc (478s)/moa/modelspec/store
- 3× focused repeat: 18/18 `--- PASS`, 0 FAIL, 0 `DATA RACE` (0.44–1.06s per run)

**Untouched per scope.** Trigger elicitation, backoff, frequency caps, prompt budgets, gate files, test-adjacent direct `runAutoDistill` calls.

## Open loops

- The distillWG fix is edited and verified but no commit/diff-accept is recorded in the transcript — land it (diff review flow or direct commit), then rebuild/redeploy the daemon binary (current daemon predates the fix).
- Decide whether `maybeAutoLand` goroutines (normal-path spawn from `drainRun` + per-diff fan-outs inside `recoverPendingDiffs`) should also be drained by `Wait()`, knowing this converts restart-interruptible design into joined lifecycle — a behavior change requiring its own pass.