> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Diff #42 Accepted Landed + advisory-slash.spec.ts Robustness (epoch 28)

Session span: odo auto-land pipeline for diff #42 (epoch-28 P0/P1/P2 fix pack, patch `5aa873ebffeeaa9d`), interrupted by user restart mid-panel, accepted manually, then the root-caused test flake was fixed.

## Timeline & decisions

| Time | Event |
|---|---|
| 16:08:54 | Diff #42 auto-land → verify (first pass: all green) |
| 16:22:26 | Verify passed → panel started (seq 11068) |
| ~16:22:30 | **User restart** interrupted panel; zero leg verdicts recorded |
| 16:24:37 | Daemon recovered; main advanced `1ea4f11 → c3de11e` (wiki distill only) → `refresh_attempted outcome=clean`, patch bytes unchanged → verify re-entered (seq 11071) |
| 16:29:54 | `auto_land_blocked reason=verify_failed` (seq 11099): 117 e2e, 1 failed / 116 passed (5.1 min). Failure: `advisory-slash.spec.ts:113 › /panel clears composer… while panel consults` |

**Decision: accept #42 instead of rerun.** The block was a load flake, not a code defect:
- Identical patch bytes passed full verify at 16:08–16:22.
- Failing spec alone: 6/6 (18s); failing case `repeat-each=5`: 5/5 (19.2s).
- Patch did not touch advisory/composer paths (GUI changes were only fixtures landing counter + switch-cache spec).
- #42 store state stayed **pending** → manual accept channel open (GUI Review inbox Accept → `accept_diff` IPC; no CLI subcommand).
- Rerunning verify+panel (~8.5 min) had zero information gain and re-exposed the #41 risk: glm fabricated rejection → `panel_mixed` → status rejected → accept channel dead → manual land + ledger repair.

User accepted (seq 11202). **#42 landed as commit `8a16ba5`**, base refresh clean (`c3de11e → c3de11e`).

## Code changes

**Diff #42 content** (7 files, +431/−19) — all four third-party review findings fixed:
- P0: accept rollback destroying user uncommitted changes → dirty-path guards ×2; refusal no longer wraps `errBaseStale`.
- P1: `retireRun` closing wrong worktree across runs → match diff's own run by worktreePath.
- P2: large-file preview full memory read → `io.LimitReader` streaming.
- switch-cache test race → landing counter + wait-for-commit signal.

**Flake root cause** (mechanism, from code path — the 16:29 failure body was truncated by `capDetail` and not journaled):
- Journaled question/answer bubbles, N/M heartbeat, queue-dock rows (`deriveParkedGoals(events)`) all arrive via the event poll loop; `POLL_INTERVAL_IDLE_MS = 1500`, and during advisory hold `streaming:false` → constant 1.5s idle cadence.
- Default expect window is 5s; under post-restart full 117-test load, one crushed tick kills the assertion.

**Fix** (`gui/e2e/advisory-slash.spec.ts`, +24/−15):
- File-level `const POLL = { timeout: 12_000 }` with a comment recording mechanism + 2026-08-24 verify observation — follows existing convention family (`REFRESH = {timeout: 12_000}` in four specs, `POLL` in loop.spec); no second convention (e.g. `test.setTimeout`) introduced.
- 15 poll-dependent assertions got the window: `.bubble-user` ×2, `.bubble-agent` ×3, heartbeat chain (`1/3 back`, legs ×3, `2/3 back`, error), spinner dissipation, queue-chip/queue-row ×2 sets. Per user rule, all same-class sites audited, not only the failed line.
- Local RPC-resolution assertions (textarea values, error-banner, park aria) untouched — they bypass the poll loop.

**Verification**: spec ×3 (`--repeat-each=3`) → **18/18 passed** (53.6s); `npx tsc --noEmit` exit 0; `git status` shows only that file. Change left in worktree; run-end drain will emit the next diff through verify (tsc + full 117 playwright) + panel.

## Open loops

- advisory-slash.spec.ts robustness change is in the worktree, pending run-end drain → next diff → verify (tsc + 117 playwright) + panel adjudication.
- Running daemon (PID 768) is on the old image; #42's guard logic takes effect only after redeploy + restart (rule: swap binary, then kill).
- Claimed `App.tsx` warm-effect cache pollution: pattern exists but not observed under instrumentation; needs a dedicated repro to justify a fix.
- hermes-agent daemon restart decision pending since epoch-23/27.
- CLI old-binary worktree-path bootstrap hang: unfixed.
- Optional test hardening backlog: tail-content assertion, `==""` branch coverage.