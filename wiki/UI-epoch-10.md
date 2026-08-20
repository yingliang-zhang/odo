# Diff adjudication #21–#26, GUI lock fix, and daemon restart storm — epoch 10

## Decisions

- **#21 rejected directly via daemon socket** (`.odo/odo.sock` → `reject_diff{21}` → `{"ok":true}`, store `rejected`, journal seq 3915, `head_sha=9c7e52d`). GUI was unclickable due to the very bug being fixed; daemon-side `handleDiffAction` has no pipeline lock (only acceptMu + status checks), so human reject always passes. Goal: pending empty at restart — no panel rounds saved, achieved.
- **#22 accepted** (user): UI switch-cache + two stale-test fixes. Its `verify_failed` block was a stale pre-rebase verdict; snapshot rebased to `9c7e52d`, byte-identical with worktree staging; main verified green after landing.
- **#25 accepted**: restart pending-diffs dedup (Plan A). Its block was `protected_path` policy (daemon core paths never auto-land), not a quality rejection. Recommended order #22 → #25: #22 freshly rebased, disjoint `server.go` regions (`poll/AfterSeq` vs `NewServer` recovery); `autoland.go` touched only by #25.
- **GUI lock bug root cause**: `auto_revise_product` journal rows are pure bookkeeping (drainRun Fix B1, lets `supersedeChain` find the repair product). `derivePipelineStates.latestChainRow` treated the latest auto_panel row as state → `default` branch → `in_flight` → `pipelineHumanLocked=true` → Accept/Reject/Review disabled; the row is never superseded on the origin, so the lock was permanent. Daemon (`loop_run.go:831`) semantics: "revise done, product self-manages" — GUI was misaligned. Fix: skip such rows; origin falls back to round row → `revise` phase → human-decidable (design intentionally keeps "accept origin to terminate ladder early").
- **User policy**: human diff review declared low-value; future reviews fully automatic. Doctrine change was implemented and green but lost before diff registration (see Lost work).

## Code changes

GUI lock fix (worktree; now diff **#26**, pending):
- `gui/src/pipeline.ts` — `latestChainRow` skips `auto_revise_product`.
- `gui/src/types.ts` — `EventPayload.product_diff_id` typed (row already carried it).
- `gui/src/pipeline.test.ts` — regression test replicating the real 21→22 chain; asserts 21=revise, 22=blocked, no locks.
- `docs/design/pipeline-indicator-lock.md` — "exclusion clause" section against regression.

Verification at time of fix: `tsc --noEmit` 0 errors; `vitest run` 8 files / 98 tests green. Pre-existing gofmt debt in `loop_audit`/`loop_journal` (epoch-4) untouched.

Deployment state (verified):
- New daemon binary from 22:56 carries #22+#24+#25; sha parity across both binary locations (`8beaf40f…`). Deploy rule followed: swap binary, then kill; GUI auto-restarts from `<project>/odo` in 1–2s.
- #25 dedup live, self-evidenced in log: `recover-pending-diffs: 1/1 pending diffs already adjudicated — skipping their re-fire` — restart with zero re-fire.
- main @ `c166ea8` green: `TestGetSettings` + `TestPhantomDiffVerdictBlocksAutoLand`, 5.2s ok (fixes carried by #22).

## Daemon restart storm ("resend" bubbles)

The "daemon restarted while this request was in flight" message is an orphan marker stamped on in-flight requests when the daemon dies; bubbles persist in chat history and do not indicate current failure. Daemon died 4 times:

| Time | Cause | Nature |
|---|---|---|
| 12:10, 15:18 | Human SIGTERM | Normal restarts |
| 15:36:49 | **modernc.org/sqlite SIGBUS** — WAL recovery mmap crash, `daemon.log:2320`; same signature 2× on 08-11 | Only unexpected death; low-frequency but recurring |
| 22:56:50, 22:57:04 | Deploy binary swap + kill | Deployment tail |

Last resend event 22:56:51; daemon PID 68480 stable since. SIGBUS remedies: upgrade `modernc.org/sqlite`, or add `_pragma mmap_size=0` to the connection string to disable WAL mmap.

## Lost work

The full-auto-review doctrine change (server.go execution-layer edits, `TestAutoLandCheck` table update, `riskNotes` assertions in review_test, design-doc + prefs-comment updates) was complete with suite green at 22:11 but never registered as a diff; worktrees were wiped clean (no commit/stash) when the #25 accept advanced main. Fully recoverable by replaying session transcript `sessions/6a8715dd-60842bf07b67/output.txt` (1.78MB, complete write/edit payloads).

## Open loops

- #26 (GUI lock fix) pending — user accept in GUI (blocked phase, clickable).
- Rebuild the lost doctrine change from transcript and resubmit as a new diff? Awaiting user decision.
- modernc.org/sqlite SIGBUS recurrence — no ticket yet; choose remediation (upgrade vs `_pragma mmap_size=0`).