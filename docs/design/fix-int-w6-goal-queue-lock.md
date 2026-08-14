# DESIGN LOCK — Wave 6: goal-queue → park-and-switch (design-only)

Tri-model design consolidated 2026-08-14 (K3/GLM/DSF). All three legs
independently reached: (a) drain-to-completion (no suspend — OMP owns the
loop; no cancel — that already exists as handleCancel), journal-derived
per-conversation FIFO queue, fully-automatic dequeue on runDone, panel
rejection does NOT block the queue, no timeout, cap 8.

Dissent recorded:
- GLM "park is the wrong verb for the goal; it's the right verb for the
  diff" — partially adopted: the journal flag stays `park:true` (the
  audit's locked mapping says `steer`/`parked`), but the daemon symbols
  name the *goal* lifecycle: `parkGoal` (enqueue), `activateParkedGoal`
  (dequeue + start). The run lifecycle stays `draining → finished`.
- K3's `parked_goal_dropped` action + `resume_parked_goal`/`drop_parked_
  goal` IPC commands — adopted 2/3 (GLM also wants manual drop; DSF has
  no manual path — the queue auto-drains and the human rejects the diff
  if undesired). The manual commands are cheap additive IPC and serve
  the "clean the junk drawer" use case.

## Contract changes (all additive-optional-with-absence, ADR-0002 immune)

| # | Surface | Change | Flag |
|---|---|---|---|
| 1 | `user_message` payload | `"park": true` — the durable goal. Text verbatim; no receipt/recall keys (steer-path journaler). `steer` and `park` are mutually exclusive — refuse pre-journal. | `[CONTRACT additive]` |
| 2 | `review_action{action:"run_prompt"}` | new `origin` value `"parked_goal"` (existing: `"continuation"`, `"retry"`); additive key `"goal_seqs": [N]` linking the dequeue receipt to the consumed park row(s). | `[CONTRACT additive]` |
| 3 | `review_action` | new action `"parked_goal_dropped"` with `"goal_seq": N` (no actor — human decision). | `[CONTRACT new action value]` |
| 4 | IPC | `Request.Park bool`; new cmds `resume_parked_goal`, `drop_parked_goal` (+ `Request.GoalSeq int`); `Response` addends `parked` (queue depth); `pending_counts` gains `ParkedGoals map[int64]int` keyed by workstream; prefs `parked_goals: auto|manual` (default auto). | `[CONTRACT]` |

NO new event type. The park decision = `user_message{park:true}`; the
dequeue fact = `run_prompt{origin:"parked_goal", goal_seqs}`; the drop
decision = `parked_goal_dropped{goal_seq}`. A `goal_parked` event would
state what `park:true` already states (steer-flag precedent).

## Semantics

**Park = enqueue goal; switch = drain-then-activate.** The current run
drains to completion (diff journaled + maybeAutoLand spawned in a
goroutine — panel spend does NOT hold s.mu and does NOT delay the next
run). The parked goal starts the instant the current run finishes.

**Queue data model:**
- Per-conversation FIFO by journal `seq` (the atomic ordering domain).
- Cap: `goalQueueCap = 8`. Over-cap → pre-journal error `"parked goal
  queue full (8)"` (fail-loud, never silently drop a human message).
- Runtime state: `Server.parked map[int64][]parkedGoal` guarded by `s.mu`;
  `type parkedGoal struct { seq int; text string }`. The journal is the
  authority; the map is a hot cache seeded at boot by
  `deriveParkedGoals(events)` — `user_message{park:true}` rows minus
  seqs consumed by any `run_prompt{goal_seqs}` or
  `parked_goal_dropped{goal_seq}` row. Daemon kill mid-queue → restart
  → parked goals resume their wait; nothing was ever memory-only.

**Dequeue: fully automatic, three call sites:**
1. On `runDone` — `drainRun`'s terminal tail: if the conversation has a
   parked goal and no active run, start the oldest
   (`s.maybeDequeueParkedGoal(ctx, conversationID)`).
2. On send to a free conversation — a parked goal sent when no run is
   active starts immediately.
3. On daemon startup — `s.recoverParkedGoals(ctx)` scans active
   conversations and dequeues the oldest parked goal for each free one
   (the durable-inbox's whole point: restart mid-run no longer loses the
   human's queued goal).

**Steer vs park precedence:** steer continuations outrank parked goals
at each drain — a steer extends the goal thread it was typed against, so
`drainRun` fires at most one continuation OR one parked-goal activation
per finished run, never both; the parked head survives to the next drain.

**Errored runs do not auto-activate.** The queue holds and
`journalRunAdvisory` fires: "odo: N parked goal(s) remain queued — the
last run errored; review it, then resume_parked_goal or wait for the
next successful run."

**Pipeline rejection does not block the queue.** A `panel_mixed` (or any
`auto_land_blocked` reason) leaves the diff `pending` for the human; the
parked goal's own diff gets its own pipeline evaluation later. The goal
queue and the review queue are independent.

**Stuck goal = its diff sits pending.** No timeout. The human rejects
the diff if undesired; the goal is done either way. No re-queue.

## Consumer-safety

- `ComputeAutonomy` — no change (skips non-review_action; parked rows
  die at the first filter; run_prompt/parked_goal_dropped hit continue).
  Parked goals cannot move human-streak math. Regression-pinned.
- `distillRender` — parked `user_message` renders like any user turn
  (a parked goal IS a user ask; the note should summarize it).
  `run_prompt{origin:"parked_goal", actor:"auto_panel"}` rows are
  fold-excluded by `foldExcludedReviewAction` (already lists run_prompt).
  Manual-resume rows (no actor) render a harmless one-liner.
- `collectReplayTurns` — one real guard: a WAITING parked goal (park:true
  row whose seq is not yet consumed by run_prompt/parked_goal_dropped)
  must NOT replay into intervening runs' prompts (the M18 repair-prompt
  hazard). Add a first-pass exclusion over the same event slice. Consumed
  parks replay normally (the goal ran; the text is honest history).
- `slashConversation` — left unchanged (/panel is read-only; seeing
  "you also parked X" is signal, not hazard).
- Recall audit / contradiction scan / todo ingest — structurally
  untouched (parked rows carry no recall keys; unknown keys ignored).

## New file

`internal/ipc/parked.go`:
- `type parkedGoal struct { seq int; text string }`
- `func deriveParkedGoals(events []store.Event) []parkedGoal` — the
  journal-derived fold (park:true minus consumed).
- `func (s *Server) maybeDequeueParkedGoal(ctx, conversationID int64)`
- `func (s *Server) recoverParkedGoals(ctx)` — boot scan.
- `func (s *Server) handleResumeParkedGoal(ctx, req Request) (Response, error)`
- `func (s *Server) handleDropParkedGoal(ctx, req Request) (Response, error)`

## Tests

`internal/ipc/parked_test.go` (new):
- `TestParkGoalQueues` — park a goal mid-run → journal row, queue depth 1
- `TestParkedGoalAutoActivatesOnDrain` — run finishes → parked goal starts
- `TestParkedGoalFIFOOrder` — park 3 goals → activate in seq order
- `TestParkedGoalCapRejects` — 9th park → error, no journal row
- `TestParkedGoalSurvivesRestart` — seed journal, new Server → recover
- `TestParkedGoalDroppedJournals` — drop → parked_goal_dropped row, queue empty
- `TestParkedGoalConsumerSafety` — ComputeAutonomy/distillRender/replay
  unchanged with parked rows in the journal
- `TestReplayExcludesWaitingParkedGoal` — waiting park not replayed into
  intervening run
- `TestSteerAndParkMutuallyExclusive` — both flags → pre-journal error
- `TestParkedGoalDoesNotBlockOnPanelRejection` — panel_mixed on current
  diff → parked goal still starts

## Docs

- `docs/design/fix-int-w6-goal-queue-lock.md` (this lock).
- `docs/adr/0005-goal-queue-park-and-switch.md` (NEW — this is a
  daemon-behavior ADR, not just additive keys; Status: Accepted 2026-08-14).
- `docs/milestones/m18-settlement-ladder.md`: ~10-line appendix (W6 keys).
- README: one wave-history row.

## Hard rules

- No git add/commit. Touch: internal/ipc/{parked.go (new), server.go,
  settle.go, protocol.go, autonomy.go} + their tests + docs listed.
- No GUI this wave. No new deps.
- If a locked step contradicts the code, STOP and report.
- Verify: `go build ./... && go vet ./internal/... && go test ./internal/ipc/ -count=1`
