> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Odo: Fix Laggy Project/Workstream Switching

## Problem
Switching between projects/workstreams felt janky, especially while a prompt was running. Investigation traced the stall across GUI → IPC → daemon.

## Root causes identified
1. **Full journal replay per switch (primary bottleneck)** — every bootstrap called `ListEvents(ctx, c.ID, 0)`, re-replaying the entire event journal on each switch.
2. **Daemon global lock held over I/O** — `handlePollEvents`/`handlePendingCounts` ran SQL and file reads while holding `s.mu`, serializing everything during active runs.
3. **Unconditional `generateAgentsMD` write** — rewrote the file even when content was unchanged.
4. **No GUI-side caching** — each switch re-fetched everything and showed a skeleton screen.

## Decisions
- **Stale-while-revalidate on the GUI**: a per-conversation LRU cache renders instantly on click; the arriving bootstrap merges by seq. Safe because the journal is append-only, so seq-merge is lossless.
- **Incremental bootstrap**: GUI sends `afterSeq` + `conversationId`; daemon replays only events after that seq. Full replay remains the fallback for cold cache.
- **Lock restructuring verified safe**: `latestDiffInfo`/`pendingDiffInfos` only touch store + files (bootstrap already called them lock-free), so SQL/file reads moved out of `s.mu`; snapshot taken under lock, `defer` retained for panic safety.
- **Optimistic flip with real rollback**: review round caught an incomplete rollback — final version restores `workstream`/`workstreamNameRef`, uses a `rootFlipped` boolean computed at flip time (no closure comparison), leaves `bootstrappedRef`/`prevDiffsCountRef`/`diff` to `applyBootstrap`, guards `handleSend` in-flight responses with captured `(cid, root)`, and avoids ref mutation inside state updaters.
- Default alias detection made explicit ("bootstrap landing without a workstream param") instead of relying on `name === "main"`.

## Code changes
- **GUI**
  - `gui/src/switch_cache.ts` (new): pure, testable LRU journal cache + workstream→conversation resolution table + seq-based merge.
  - `gui/src/App.tsx`: replaced local `mergeEvents` with import; `applyBootstrap` merges by seq for same-conversation landings; cache-warming effect; both switch handlers rewritten (optimistic flip + full rollback); `handleSend` in-flight guard.
  - `gui/src/api.ts`: bootstrap passes `afterSeq`/`conversationId`.
- **Rust bridge** — `lib.rs`: passthrough of `afterSeq`/`conversationId`.
- **Daemon** — `server.go`: `handlePollEvents`/`handlePendingCounts` snapshot-in-lock / I/O-outside-lock; `generateAgentsMD` skips write when content identical; bootstrap honors `afterSeq`.
- **Tests**
  - `switch_cache` vitest (LRU, truncation, alias, merge).
  - `server_test.go`: bootstrap `afterSeq` incremental test.
  - e2e fixture: controllable bootstrap delay; new spec covering (a) instant render from cache on repeat switch, (b) rollback on switch failure.

## Verification results
- Daemon: new `afterSeq` test passes; `-race` subset (poll/pending) passes; full suite passes.
- Rust: 6/6 pass.
- GUI: vitest 109/109, `tsc` clean.
- E2E: 115/115 green, then two new behavior tests added — instant-render test **passed**, rollback test **failed** in the final run.
- Auto-land **blocked** (`verify_failed`) due to the failing rollback e2e.

## Open loops
- Rollback-on-failure e2e test fails; cause not yet diagnosed from the failure output — landing is blocked until it passes.
- No before/after latency numbers captured; perceived-smoothness improvement is unmeasured beyond test evidence.