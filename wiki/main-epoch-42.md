# Odo Backend Review & Fix Session (2026-08-25)

## Context

Session began with a review report covering the odo daemon (Go IPC server) and GUI (React/Tauri). All four reported code findings were verified against source, then fixed in order of risk: **#2 → #3 → #4 → #1**. The run ended with the diff **rejected** for touching a protected wiki path (see Open loops).

## Verified findings (pre-fix state)

| # | Sev | Issue | Evidence |
|---|-----|-------|----------|
| 1 | P1 | Memory/pins crash recovery loses consumed ops: recovery folds only the single conversation's journal; if another workstream lands first, foreign-state skip discards the intent permanently | `server.go:727-733`, `audit_fixes_test.go:562` |
| 2 | P1 | Auto-distill goroutines outside Server lifecycle: `go s.runAutoDistill(...)` (`auto.go:386`) and timer callbacks untracked by `Server.wg`; same for `recoverPendingDiffs` (`server.go:396`). Reproduced via `TestAutoUrgentUpgradeSupersedesIdle` — in-flight distill writes wiki/journal after TempDir cleanup and store close (`database is closed`) | race logs, 1-in-3 failure rate |
| 3 | P1 | delete_workstream vs bootstrap atomicity gap: `deletingWs` set only inside the `GetActiveConversation` success branch (`server.go:892-933`); a conversationless lane allows bootstrap → delete → bootstrap-creates-conversation under a deleted workstream | source trace |
| 4 | P2 | Loop file read unbounded: `os.Stat` → `readWithinDir` (unbounded `os.ReadFile`) → post-hoc length check (`loop.go:402-423`); file growth in the window forces full allocation before the cap is enforced | source trace |

## Fixes implemented

- **#2 Lifecycle**: auto-distill spawns and timer callbacks wrapped into the Server waitgroup; `recoverPendingDiffs` fan-out tracked the same way; `rig.stop` drains in-flight work. Regression test added. Verified: `-race` ×3 pass on the former reproducer; drain measured blocking 3.7–3.9s.
- **#3 Atomicity**: bootstrap's create section moved under the same lock with a `deletingWs` guard; delete path hoists the flag unconditionally before the conversation lookup. Regression tests cover the no-conversation lane and mid-delete bootstrap race.
- **#4 Bounded read**: `readWithinDir` replaced with a capped reader that enforces the size limit during read (before allocation); unit test added; `errors` import fixed after an edit broke the import block.
- **#1 Project-level outbox** (new `memory_outbox.go` + `memory_outbox_test.go`):
  - Boot-time replayer across all workstreams, globally ordered by `store.Event.ID`.
  - Each unconsumed apply marker / pin receipt re-runs the original planner against the **current** file (idempotent rebase); retract filter prevents resurrecting retracted rules.
  - Two-pass project-wide collection replaced per-lane gating (`maxPinsTouch` gate was order-dependent).
  - Explanation-set gating: a file hash is writable only if explained by journal `after_sha` values or outbox receipts (`mem/pins_after_sha`) — protects manual edits from being overwritten or revived.
  - Applied/conflict accounting via `receipt_seq`; wired into `NewServer` startup. GUI confirmed safe: unknown memory layers fall through to the generic chip; `types.ts` layer comment updated.
- **Docs correction in flight**: wiki `run-lifecycle` topic previously stated the delete race was "accepted" — that claim is now obsolete.

## Regression status

- Baseline (pre-session): `go test ./...` all pass, IPC suite ~575s; GUI 166 tests / 12 files pass (React `act(...)` warning noise); production build passes, main bundle 625.18 kB (>500 kB warning); Tauri 6 tests pass.
- Post-fix targeted suites: all pass (`-race` included for #2/#3/#4 and outbox tests).
- Full-suite rerun: one background `go test ./...` run failed at exactly 600.45s — diagnosed as the default 10-minute `-timeout` (new boot fold + drain pushed the ~575s baseline over the threshold), not a deadlock; rerun with extended timeout started, result pending at session end.

## GUI analysis (not yet acted on)

Confirmed against real Hermes Desktop (`~/.hermes/hermes-agent/apps/desktop`). Gap list: no transcript windowing (full-history filter/group/map per event), monolithic 2288-line App.tsx state, right pane unmount/remount instead of keep-alive, per-pointermove resize setState, animating sidebar width 240→48 relayouts transcript, boxed card language vs flat, six product concepts crammed into a 380px tab strip. Visual incoherence root cause: `gui/src/styles/app.css:56` mixes Apple HIG radii, Multica type scale, Hermes stroke/shadow, Apple motion; `tailwind.css:5` mid-migration with legacy overrides winning cascade. Recommended order: perf fixes → durable pages (Wiki/Memory/Skills/Ledger) vs working panes (Changes/Review) → single design contract → chat column 760–840px, flat agent replies, fewer badges.

## Key decisions

- Fix ordering by expected value: race with stable reproducer first; project-level outbox last (heaviest, new durable semantics).
- Replayer replays **planner**, not raw content — rebases intents onto foreign current state instead of skipping them.
- Explanation-set gate chosen over hash-equality gate so manual file edits neither get clobbered nor cause rule resurrection.

## Open loops

- **Diff rejected at registration**: the run's diff touched protected path `wiki/topics/memory-distill.md` (invariant: agents never write memory). Entire patch rejected, worktree retired; full patch preserved at `/Users/yingliangzhang/Projects/odo/.odo/diffs/6a8d8993-68af79915939.diff`. Must strip the wiki hunks (including the `run-lifecycle` edit) and resend the remaining Go/GUI work.
- Wiki topic updates (run-lifecycle correction, memory-distill) must land through the daemon's own distill/wiki-commit pipeline, not agent edits.
- Full `go test ./...` and full `go test -race ./internal/ipc/...` with extended `-timeout` — launched, results not yet observed.
- GUI second-tier work not started: transcript windowing, panel keep-alive, rAF resize, sidebar animation, design-contract consolidation, durable-pages information architecture.
- GUI main bundle 625.18 kB > 500 kB threshold and `act(...)` warning noise unaddressed.