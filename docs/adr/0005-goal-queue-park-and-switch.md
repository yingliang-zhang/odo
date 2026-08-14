# ADR-0005: Goal-Queue Park-and-Switch

**Status:** Accepted (2026-08-14)  
**Deciders:** Tri-model design (K3/GLM/DSF), orchestrator consolidation  
**Tags:** daemon, run-loop, journal-contract

## Context

Odo runs one coding agent per conversation. A new goal mid-run either
queues behind the current run (steer continuation) or replaces it (cancel).
The user's vision: "long autonomy without interruption — parked async
review replaces the synchronous gate." The 2026-08-13 harness audit
found deepseek-harness's `Agent.steer/inject/whenIdle` durable-inbox
pattern (P0#3) directly matches this vision.

## Decision

**Park = enqueue goal; switch = drain-then-activate.** The current run
drains to completion (no suspend — OMP owns the agent loop and exposes
no pause/resume; no cancel — that already exists as handleCancel). The
parked goal starts the instant the current run finishes.

The durable queue is journal-derived (user_message{park:true} minus
consumed seqs), per-conversation FIFO, capped at 8. Dequeue is fully
automatic on runDone / send-to-free-conversation / daemon startup. Panel
rejection does NOT block the queue — the review queue and the goal queue
are independent.

## Alternatives Considered

- **Suspend (b):** rejected — OMP has no pause/resume API; a SIGKILL
  would wedge byConv forever (no terminal event); no checkpoint exists.
- **Cancel (c):** rejected — already exists as handleCancel; making
  "park" = "cancel" would turn every new goal into a half-baked diff in
  the review queue — the synchronous gate the vision retires.
- **Cross-conversation queue table:** rejected — per-conversation queues
  + the existing maxConcurrent cap (default 4) already serve the
  "park on A while working on B" use case without a new table.

## Consequences

- Four additive [CONTRACT] changes (user_message park:true, run_prompt
  origin:"parked_goal" + goal_seqs, review_action "parked_goal_dropped",
  IPC Request.Park + resume/drop cmds + pending_counts parked_goals).
  Zero new event types (ADR-0002 immune).
- `collectReplayTurns` gains a waiting-park exclusion (a waiting parked
  goal must not replay into intervening runs — the M18 repair-prompt
  hazard).
- `ComputeAutonomy` unchanged (parked rows die at the first filter;
  run_prompt/parked_goal_dropped hit continue).
- ADR-0002 (fresh journal schema) preserved: all changes additive-
  optional-with-absence.

## Implementation

Deferred to a future implementation wave. This ADR + the design lock
(`docs/design/fix-int-w6-goal-queue-lock.md`) are the design artifacts.
