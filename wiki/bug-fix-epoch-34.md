> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# W4.5 Revise — Pin `ODO_STUB_SCALE=1` in Missed WINDOW-Class Test

## Context

- W4.5 diff (42 files, +262/-11, `stubSleepPreamble` seam) landed clean; `internal/ipc` suite dropped from 840–865s to ~516s.
- Verify failed on exactly one test: `TestReviewDuringLiveRunKeepsLiveRun` (`internal/ipc/server_test.go:662`) — `pollUntilDone: first poll should report agent_running=true`; the first poll landed after the stub wrapper had already completed.
- Root cause confirmed: WINDOW-class tests (those interleaving with a mid-flight run) must pin `ODO_STUB_SCALE=1` beside their wrapper; this test was missing it. At scale=0.15, `slowStubWrapper` sleeps only ~0.45s, but between run-2 send and the second `pollUntilDone`'s first poll sits real work (AcceptDiff git apply + journal), which exceeds 0.45s — run 2 finished early → `agent_running=false`.

## Key Decisions

- **Worktree mismatch discovered and routed around**: the session was dispatched to clean worktree `6a94f6b3` (HEAD == main, zero W4.5 traces), while the actual changeset lived in sibling dirty worktree `6a94ebb6`. The fix was applied in `6a94ebb6`; the worktree was left dirty per instructions.
- **Strictly minimal fix**: one-line env pin, no production code, everything else byte-identical; a full audit was used to check for other misses rather than widening the change.
- **Stronger focus gate**: 3 fresh-process runs instead of a single `-count=3` invocation (better process isolation for a timing-window test).

## Code Change

`internal/ipc/server_test.go:668`, inside `TestReviewDuringLiveRunKeepsLiveRun` — the only edit:

```go
t.Setenv("ODO_STUB_SCALE", "1") // W4.5 WINDOW: the run-1 review must land while run 2 is still in flight (~3s span)
```

Comment format matches the three existing `// W4.5 WINDOW:` pins in `concurrent_test.go`.

## WINDOW-Class Audit (39 Touched Test Files)

Audit patterns: `pollUntilDone` first-poll contract, `.finished`/`byConv`/`runs[]` checks, `CmdCancel`, mid-run steering, test-side `time.Sleep`, distill-in-flight `waitForCond`. Result: **39 files checked / 1 pin needed (the reported test, fixed) / 0 additional pins**.

- `concurrent_test.go`: 3 WINDOW pins already present (lead-ins land inside ~6s distill span); other concurrent tests poll ms after send — 0.45s window ample.
- `server_test.go` steer/cancel tests (`TestSteering`, `TestCancelRun`, `TestSteerDroppedOnAgentError`, `TestFalseStopRetryConsumesSteers`): operations land ms after send; design margin ≥150ms.
- `auto_test.go` distill-in-flight `waitForCond`: flag set before one-shot; assertions within ms, 0.6s window.
- `loop_test.go` (`TestLoopHumanSendSuspendsMidLoopRun`, `TestLoopRestartMidRunSuspends`): ms-level intervention; slow=5s → 0.75s scaled window ample.
- Autoland `TestLandWGDrainPinFencesWait`, audit_fixes moa tally, slashctx/autoland httptest handlers: channel-gated or server-side Go sleeps — unaffected by `ODO_STUB_SCALE`.
- settle / revise_product_missing / parked / run_usage: use `pollDone` (no first-poll contract) + poll-until-condition ladders; no mid-flight assertions.

## Verification (Gate Tails)

- Focus gate, 3 fresh processes:
  ```
  ok github.com/yingliang-zhang/odo/internal/ipc 8.628s   (run 1, cold cache 15.91 real)
  ok github.com/yingliang-zhang/odo/internal/ipc 7.742s   (run 2, 8.90 real)
  ok github.com/yingliang-zhang/odo/internal/ipc 7.724s   (run 3, 8.84 real)
  ```
- Full suite `go test ./internal/ipc/ -count=1 -timeout=20m`:
  ```
  ok  github.com/yingliang-zhang/odo/internal/ipc  507.844s
  508.90 real  116.49 user  141.71 sys  EXIT=0
  ```
  ~516s target held (measured 507.8s).

Risk: single-line env pin, zero production impact, all other test behavior unchanged.

## Open loops

- `liveness_test.go:128` stale comment — still says "stub finishes in ~1s" per unscaled semantics; under scaling the ready-window actually strengthens (~half → ~93%). Assertion still holds; comment drift only, out of scope this round.
- Worktree `6a94ebb6` remains dirty with the W4.5 changeset plus this one-line pin; landing/commit awaits user decision (prior auto-land was blocked on the verify failure this fix resolves).
- Dispatcher attached the session to clean worktree `6a94f6b3` instead of the dirty `6a94ebb6` holding the changeset — root cause in worktree dispatch/attachment logic worth a look.