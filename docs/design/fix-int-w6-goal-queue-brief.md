# Frozen Brief — Tri-Model Design: goal-queue → park-and-switch (audit P0#3)

## Context

Odo (`~/Projects/odo`, HEAD `8ad385c`) is a Go daemon that runs ONE coding
agent at a time per conversation. Today's steering (A2-lite, server.go
~1180): a `steer:true` user_message journals the text and queues it onto the
active run's `meta.queuedSteers`; when the run drains, a continuation run
starts with the queued text as the prompt. There is no notion of a "parked"
goal — a new goal mid-run either queues behind the current run (steer) or
replaces it (cancel + new send). The user's vision (from the project wiki):
"long autonomy without interruption — ladder replaces human diff review,
parked async review replaces the synchronous gate."

A 2026-08-13 tri-model harness audit found deepseek-harness's
`Agent.steer / Agent.inject / Agent.whenIdle` durable-inbox pattern (audit
P0#3) directly matches Odo's parked-async-review vision. The locked take:

> Durable async steering / inbox: daemon run loop + journal `steer`/`parked`
> events. Map: directly matches the goal-queue / park-and-switch design
> item. Cost S–M. Priority P0.

## Design questions (answer each: options + recommendation + exact symbols)

**Q1 — Park semantics.** What does "park the current goal" mean
mechanically? Options: (a) the current run drains to completion (diff
produced + journaled + auto-land pipeline fires), THEN the parked goal
starts — no interruption; (b) the current run is suspended (SIGSTOP /
checkpoint / adapter pause), the parked goal runs, and the suspended run
resumes later; (c) the current run is cancelled (SIGKILL, partial diff
kept per ADR-0001), the parked goal starts. Which one, and why? Weigh:
the agent loop is OMP-owned (Odo does NOT control the agent's internal
state — no pause/resume API exists); a killed run's partial diff enters
the review queue (work is not lost); a drained run's auto-land pipeline
takes 10–60s (panel spend). Is "park" even the right verb, or is it
"queue the next goal and let the current one finish"?

**Q2 — Goal-queue data model.** Is the parked goal a new journal event kind
(`goal_queued` / `steer` with a `parked` flag) or a new payload key on the
existing steer user_message? Is the queue per-conversation or
cross-conversation (the user's "parked async review" vision implies the
human can park a goal on conversation A while working on B)? Is the queue
FIFO or priority? How many parked goals can coexist (bounded? unbounded
with a cap)?

**Q3 — Drain / activate.** When the current run finishes, what triggers the
parked goal to start? Is it automatic (daemon run loop checks the queue
on `runDone`) or does it need a human action (user clicks "resume parked
goal")? The vision says "without interruption" — does that mean fully
automatic, or does the human approve the dequeue? What happens if the
auto-land pipeline rejects the current run's diff (panel_mixed) — does the
parked goal still start, or does the rejection block the queue until the
human resolves it?

**Q4 — Journal contract.** What exact event types / payload keys are
introduced? Flag all `[CONTRACT]`. Prove consumer-safety (the usual
discipline). Is a `goal_parked` event needed, or does the existing
`user_message{steer:true, queued:true}` suffice with a `parked:true` flag?
Does `ComputeAutonomy` need to know about parked goals (do they affect
human-streak math)?

**Q5 — Interaction with the auto-land pipeline.** A parked goal's run
produces a diff → the auto-land pipeline fires → if it blocks (panel_mixed,
base_stale...), the goal is "stuck" in the review queue. Does a stuck goal
block the next parked goal from starting? Is there a "parked goal timeout"
(goal auto-rejected after N hours)? Does the human's rejection of a stuck
goal's diff dequeue it or re-queue it?

**Q6 — Tests + docs.** Name them. ADR? Milestone note?

Constraints: OMP owns the agent loop — Odo cannot pause/resume a run, only
cancel/drain; append-only journal holy; no GUI this wave (design-only);
the auto-land pipeline is the authority — a parked goal's diff goes through
the same gates; minimal diff; scope to the daemon run loop + journal.

## FINAL-MESSAGE CONTRACT (flash variant)

Many tool calls expected; the FINAL assistant message must start with
"# W6 Goal-Queue Park-and-Switch Design" and contain the complete Q1–Q6
answers, exact symbols, `[CONTRACT]` flags, test names.
