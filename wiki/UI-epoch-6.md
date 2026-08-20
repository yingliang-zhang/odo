> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Odo Auto-Land Review Frequency, Diff #11 Deploy, and Steering Verification (2026-08-19)

## Why manual diff reviews kept appearing

User accepted diff #11 yet reviews kept recurring; investigation showed the pipeline is healthy, not regressed.

- Pending queue after diff #11 landed (`d709366`): **0**. No backlog.
- Journal `auto_land_blocked` (diffs 1–11): every recent manual gate was caused by diffs touching a protected file — `internal/ipc/autoland.go`, one of nine `protectedGateFiles`. Touching any of them forces manual accept; auto-land refuses by design (fail-closed).
- Auto-land works when protected files are untouched: diffs 5 and 7 landed fully automatic; diff 4 auto-landed after one `verify_failed` retry.
- Other blocks: diff 8 (`verify_failed`, needs human eyes); diff 9 rejected (`repair_prompt_too_large`, 85KB > 64KB auto-fix cap); diffs 1–3, 6, 10, 11 (`protected_path`).
- Recent frequency explained by work focus: steer-queue / verify-gate / pipeline self-modification (diffs 10, 11 both edited `autoland.go`) — expected shape during bootstrap.

**Decision:** keep the protection surface unchanged. Loosening it would let the pipeline self-modify unsupervised. Mitigation for review fatigue: batch protected-file changes into larger single diffs.

## Landed code changes deployed

| Diff | Commit | Content |
|---|---|---|
| #10 | `7cd0848` | IME `compositionend` fix (GUI) |
| #11 | `d709366` | Daemon: verify multi-match selection + `queuedSteers` journal recovery via `recoverOpenSteers`; GUI: SteerQueue drop-confirm state preservation (touches `autoland.go` → manual accept) |

## Deploy execution & verification

Bare app restart was insufficient — all running artifacts were built 19:49 from `7cd0848`; diff #11 was in no binary. Full deploy executed and verified:

- **Pre-build check:** 38 uncommitted changes in main repo were all `wiki/` memory artifacts (daemon distill output), zero source files → build equivalent to clean HEAD `d709366`.
- **Daemon binary:** new sha `707531ac…` (old `7c21f50e…`); `daemon_restart` marker present in new, absent in old. Atomic replace (cp-to-temp + mv) of `<project>/odo` and `~/.odo/bin/odo`.
- **App:** `npm run tauri:build` 21:38 (`e2cf3c14`), `/Applications/Odo.app` replaced, restarted (old PID 71269 → new 89451).
- **Handover:** killed old daemon 71468; app auto-respawned daemon (PID 89238) from new binary; daemon survives app exit. Ordering rule: replace binaries *before* killing the daemon, else the old binary gets relaunched.
- **GUI bundle content:** Mach-O grep is unreliable (Tauri compresses embedded resources); verified via `gui/dist` source — both `steer-queue` (drop-confirm) and `compositionend` (IME) markers present.
- **Cleanup:** stale vite dev server 77641 (fix worktree `6a859a11`) terminated; stray daemon from an accidental bare `odo help` spawn self-cleaned (same incident pattern as UI-epoch-5).

## Post-restart health check (21:44, after user's own restart)

- App PID 90065; main daemon PID 90066, sha `707531ac…` = `~/.odo/bin/odo`; socket live; journal writing in real time.
- ui-message-stream project's daemon self-refreshed (PID 90078 from new `~/.odo/bin/odo`) — the previous out-of-scope straggler (71283) closed naturally.
- Pending diffs: 0. All artifacts on latest code, no leftovers.

## Steering verification

User's test message (seq 2703, `steer:true`) itself proved the full path end-to-end:

1. Enqueued during active health-check run → 2. `agent_done` (2706) triggers drainRun → 3. `run_prompt{origin:"continuation", steer_seqs:[2703]}` (2707) → 4. continuation run executed. Ledger exactly closed, no residue.

Production evidence for diff #11 ① ghost-steer recovery: seq 2641 `steer_dropped{cause:"daemon_restart", steer_seqs:[405,653,659]}` — `recoverOpenSteers` closed three historically dangling steers at the binary-swap restart instead of leaving undeletable GUI ghost rows. No new drops after the 21:39/21:44 restarts; zero unclosed steers.

Diff #11 ② drop-confirm poll-heartbeat resistance: journal-invisible, requires manual GUI test.

## Operational gotchas recorded

- Shell `kill` builtin refused PID 71468 (likely session process-tree ancestor); external `/bin/kill` bypassed it.
- Agent bash cwd had drifted into a worktree, causing repeated failed commands; absolute paths bypass.
- Bare `odo help` can spawn an unintended daemon.

## Open loops

- Drop-confirm poll-resistance (diff #11 GUI fix ②) still unverified: needs manual GUI test — during a long run, steer once, click drop (arm), let several poll ticks pass, click again; confirm state must not reset. Agent offered to launch a long steerable task for this; awaiting user decision.