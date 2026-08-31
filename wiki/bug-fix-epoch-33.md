# D9-W4 test-regression fixes & W4.5 IPC suite acceleration

## Context

Two sequential tasks in the `odo` repo, both scoped to `internal/ipc/**` only (no `settle.go`, no Tier-0, no supply-chain, worktree left intentionally dirty):

1. **D9-W4 revise** — the W4 diff (22 files, +3755/-46: learning_candidate/lint/replay/stages/canary/scoring + tests) landed clean but verify failed on 5 tests.
2. **W4.5** — the fixed suite then ran 840–865s serial and twice timed out a *zero-failure* full run at the verify gate's 14m line (raised to 20m as interim relief, commit `3763a67`), motivating a structural speedup: 840s → ≤400s with zero assertion changes.

## Task 1: D9-W4 revise — 5 test-fixture regressions

**Root cause (single class):** the W3a run_usage receipt (`memory_update{layer:"run_usage", usage_available:false}`), journaled in a defer at the `drainRun` tail so every terminal path journals it, shifts event sequences that older tests pin exactly. `TestDistillSweepsLegacyBatch` additionally races: a receipt landing inside the distill window perturbs sweep ordering, so the batch's `.odo/memory.md` write lands after the test's read.

**Key decisions:**
- **KEEP the receipt** — planned D9 behavior (lock W3: fail-soft, journaled where the events already fire). Tests were adjusted, not production code.
- Exception clause investigated and ruled out: goroutine dump of the stalled gate process showed **no drain/mutex stuck frames** — no double-fire, no wedged drain path.
- For the 4 sequence tests: update assertions to account for the trailing fail-soft run_usage row (pre-receipt prefix + a dedicated assertion that the receipt exists).
- For the distill test: await the batch's actual completion signal (poll `memory.md` with deadline) instead of racing the sweep.

**Changes:** all 5 assertion sites updated across `learner_test.go` (`TestLearnerProposesJournaled`), `server_test.go` (`TestVisibleLoopAcceptRejectRestore`, `TestNoDiffRunRetiresWorktree`, `TestStreamingVisibleLoopPreview`), `memory_autogate_test.go` (`TestDistillSweepsLegacyBatch`). 4/5 reproduced on first run; the distill test passed that run (racy, confirming the timing hypothesis).

**Outcome:** subsequent full verify runs (per the W4.5 task brief) were **zero-failure** — the fixes held; the only remaining failure mode was the suite-duration timeout.

## Task 2: W4.5 — IPC test-suite acceleration

**Measured baseline:** 843s serial; 0/50 test files used `t.Parallel()`; 37 bare `time.Sleep`; poll deadlines 10–30s (`pollDone` 20s, `waitSettle` 30s). Precedent seam: `autoIdle` override via `resolvedIdle` (auto.go:216-221), which already bypasses the `autoIdleFloor` 15s clamp for tests.

**Key decisions:**
- **One seam, existing pattern:** extend the `resolvedIdle` style — did not invent a second convention.
- **Negative-window semantics stay real:** settle-family MIXED tests keep the real 2s `settleQuiet` window ("nothing happened before X" cannot be shrunk); auto-family got 6 WINDOW tests pinned to 3s one-shot via the seam.
- **Parallel only where safe:** 44 stateless pure-fold/table/fixture tests marked `t.Parallel()`; daemon-lifecycle tests (real `NewServer`, SQLite, sockets, mock-OMP spawns, registry path env, `t.Setenv`) stay serial.
- **Valve stall treated as a real in-suite anomaly, not flake:** after-run showed 608s with one package-level FAIL (zero test-level failures; exec stack/panic; `TestDistill` standalone passed in 5.97s). 330 parked goroutines were all `t.Parallel` residency at zero CPU — external load ruled out; `waitSettle` hit its 30s timeout on the second composite wait. Fixed rather than tolerated.

**Changes:**
- `server.go`: additive production seam — `oneShotPollNs` (atomic) + `resolvedOneShotPoll` + `runOneShot` signature change; production default = real clock, reads as no-op indirection.
- 6 auto-family WINDOW tests set to 3s one-shot; settle-family negative windows untouched.
- 44 stateless tests/subtests marked `t.Parallel()` (one transcription drift noted in `learning_store` function names; re-anchored line-by-line, judged sound).
- `loop_test.go`: shortened `waitLoop` stride.
- Fix for the in-suite Valve/composite-wait stall (applied after a 5× settle-subset stress run implicated it).
- Research: three census subagents (CensusServer / CensusSettleAuto / CensusStateless) classified all `internal/ipc` test files; wrapper users outside census files enumerated before pinning.

**Measurements:**
- Before: 843s serial.
- After (seam + parallel, pre-Valve-fix): **608s (−235s)**, 1 package FAIL (the Valve stall).
- After Valve fix: settle subset green 5× consecutive.
- Final full run (`go test ./...`, `-timeout=25m` headroom, `-json` to disk) launched in background at session end — **result not yet observed**.

## Open loops

- Final full-suite wall time unobserved: the measurement run was in flight when the session ended; the ≤400s target is unconfirmed (last complete figure: 608s with a now-fixed stall).
- W4.5 final report not yet delivered: before/after top-20 duration table, per-package `ok` tails, seam-vs-`t.Parallel` inventory, one-line risk note.
- Valve stall fix verified only on the settle subset (5×) and standalone `TestDistill`; needs confirmation in the full run.
- Landing state of the D9-W4 revise changeset: auto-land went verify_failed → refresh_attempted → accept → superseded; worktree intentionally dirty — commit/land decision pending.
- `.odo-verify` gate line (14m→20m interim, commit `3763a67`): final timeout line is a deferred human decision once suite duration is known.
- Optional guardrail (suite wall-time print target): Makefile existence probed; addition not confirmed.