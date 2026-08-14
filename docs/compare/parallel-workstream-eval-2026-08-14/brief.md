# Tri-Model Evaluation: Parallel Workstream-Driven Development in Odo

You are analyst **__LEG_ID__**, one of three independent analysts. An
orchestrator consolidates your output with two other models' analyses.
Cite `path:Symbol` for every load-bearing claim; "not verified" > guess.

## HARD TIME DISCIPLINE

- Total budget ~35 min. Gather ≤15 min, then WRITE the report no matter
  what. No subagents, no builds, no test runs. greps/reads only.
- Final assistant message must START with "## A. Verdict" and contain the
  complete A–F deliverable.

## The question

The user wants to know: **Can we split the remaining Odo development
backlog into multiple workstreams and run them in parallel in the Odo GUI,
so multiple coding agents work on different features simultaneously?**

This is NOT about Odo's existing concurrency (which already supports
max_concurrent_runs=4 across workstreams). The question is about the
**practical workflow**: using Odo to develop Odo, with multiple workstreams
each assigned a feature, running agents in parallel, and the user
reviewing/approving diffs from each independently.

## Current Odo state (verify these claims by reading the code)

1. **Multi-workstream already exists**: `handleCreateWorkstream`,
   `handleListWorkstreams`, `handleBootstrap` (server.go:389,494,512).
   Each workstream gets its own conversation, journal, and worktree.

2. **Concurrency cap**: `maxConcurrentDefault = 4` (server.go:1450),
   configurable via prefs.md `max_concurrent_runs`. `activeRunCount()`
   counts non-finished runs daemon-wide (server.go:1439).

3. **W6 goal-queue park-and-switch just landed** (commit eb62b44):
   `user_message{park:true}` queues a goal per-conversation FIFO (cap 8),
   auto-dequeue on runDone. This means the user can park goals on multiple
   workstreams and they auto-start when each workstream's current run finishes.

4. **Sidebar already shows workstreams** with status dots
   (Sidebar.tsx:12-30): running (foreground), background, pending, idle.
   StatusBar shows "N background runs" with click-to-jump (StatusBar.tsx:67-77).

5. **Cross-workstream attention**: `pending_counts` returns per-workstream
   pending diffs + running workstreams (handlePendingCounts, server.go:3066).

6. **Auto-land pipeline**: each workstream's diff goes through the same
   mechanical gates + MoA panel → auto-land or human review (settle.go,
   autoland.go). Multiple workstreams can have pending diffs simultaneously.

7. **W6 parked goals**: `pending_counts` now also returns `ParkedGoals`
   per-workstream queue depth (parked.go handlePendingCounts).

## The development backlog (from the wave-history table)

| # | Task | Cost | Type | Dependencies |
|---|---|---|---|---|
| 2 | R-W1 moa resilience (retry + typed errors) | S | daemon | none |
| 3 | R-W1.5 receipts fill (request_sha16) | S | daemon | none |
| 4 | A-P0 #1 Guardian taxonomy GUI rendering | S | GUI | daemon side done (W5) |
| 5 | A-P0 #2 visible⟺logged assert residual | S | daemon | check only |
| 7 | R-W2 distill → moa migration | S | daemon | #2 |
| 8 | R-W3 learner/curator → moa | S | daemon | #7 |
| 9 | R-W4 Design-MoA consolidator | M | daemon | #2,#7,#8 |
| 10 | GUI Wave A (task registry + StatusBar + Sidebar) | M | GUI | #4 schema |
| 11 | GUI Wave B (context meter, plan comments, etc.) | S–M | GUI | #10 |

## What to evaluate

### Q1 — Is the daemon-side substrate sufficient for parallel workstream development today?

Can the user RIGHT NOW create 4 workstreams, send a different task to each,
and have 4 agents run in parallel? What works, what's missing, what breaks?

### Q2 — What GUI gaps block practical parallel development?

The user needs to monitor 4 runs, review 4 diffs, approve/reject each
independently. What GUI surfaces are missing? Consider:
- Can the user see all 4 workstreams' status at a glance?
- Can the user review a diff in workstream B while workstream A is still running?
- Can the user park a goal on workstream C while A and B are running?
- Is there a "jump to workstream" that preserves context?
- Does the StatusBar background-run chip suffice, or is it too minimal?

### Q3 — What daemon-side gaps block parallel development?

Consider:
- The concurrency cap (default 4) — is it sufficient?
- Worktree isolation — do parallel runs interfere? (each run gets its own
  worktree via `worktree.Manager`)
- Auto-land pipeline — can 4 auto-land pipelines run concurrently?
  (autoLandMu serializes ONE pipeline at a time, server.go:116)
- Memory layer isolation — do parallel runs cross-contaminate memory/wiki?
- Distill/curate — auto-distill is per-conversation, but curate is
  daemon-wide (curating bool, server.go:97). Does this block?

### Q4 — What should be built FIRST to enable this workflow?

Rank the features needed, by priority. For each:
- What gap it fills
- Cost estimate (S/M/L)
- Whether it's a prerequisite for other items
- Whether it can be done in parallel with other items

### Q5 — Risk assessment

What could go wrong with parallel self-development (using Odo to develop Odo)?
- Git conflicts between workstreams (all work on the same repo)
- Memory cross-contamination (shared memory.md/user.md)
- Wiki/epoch-note cross-contamination (shared wiki/ dir)
- Review fatigue (4 diffs to review instead of 1)
- Auto-land serialization (autoLandMu bottlenecks)
- Concurrency cap exhaustion (parked goals can't start)

### Q6 — Recommended workstream split

If the user were to split the backlog into 3–4 parallel workstreams today,
what's the optimal grouping? Consider dependencies, file overlap, and
review complexity. Give a concrete grouping with rationale.

## Deliverable structure

```
## A. Verdict  (ready-now / needs-work — with the single strongest reason)
## B. Daemon-side readiness assessment  (what works, what's missing)
## C. GUI gaps blocking parallel development  (ranked, with evidence)
## D. Prerequisites ranked  (what to build first, dependencies, costs)
## E. Risk assessment  (parallel self-development failure modes + mitigations)
## F. Recommended workstream split  (concrete grouping, rationale, file overlap matrix)
```

## Context files (READ-ONLY — do not modify anything)

- `internal/ipc/server.go` — Server struct, handleSendMessage, drainRun,
  NewServer, activeRunCount, resolveMaxConcurrent, handlePendingCounts
- `internal/ipc/parked.go` — W6 goal-queue (just landed)
- `internal/ipc/autoland.go` — autoLandMu, auto-land pipeline
- `gui/src/components/Sidebar.tsx` — workstream list, status dots
- `gui/src/components/StatusBar.tsx` — background-run chip
- `gui/src/App.tsx` — workstream state management, bootstrap, switching
- `docs/design/odo-harness-audits-summary-and-plan-2026-08-14.md` — wave plan
- `docs/compare/harness-gui-tri-model-audit-2026-08-13.md` — GUI borrow list
- `docs/adr/0005-goal-queue-park-and-switch.md` — W6 ADR
- `internal/ipc/protocol.go` — IPC Request/Response shapes
