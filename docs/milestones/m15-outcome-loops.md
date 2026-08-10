# M15 Outcome Loops — B-strategy-1 (2026-08-10)

Telemetry-only milestone: close the loop between what Odo injects and what humans accept, plus
one OMP-side probe. Nothing in this milestone auto-acts — every deliverable flags or reports;
no downweight, no retract, no auto-apply. Reads are read-only journal opens (`query_only`),
no daemon, no LLM, mirroring `odo recall audit`.

## S-1 — `odo skills audit` (flag-only)

`cmd_skills_audit.go`; joins skill injections (already receipted on `user_message.receipt`,
path → sha16 of the injected block, `internal/ipc/skills.go`) to human outcomes. It flags
underperforming skills for a human; it never downweights, retracts, or acts.

Attribution model (per conversation, joined by temporal order):

- A run is one send → one terminal event (`agent_done`/`agent_error`). The diffs table pins
  which terminals produced a reviewable diff: each diff maps to the newest unclaimed
  terminal with `created_at <= diff.created_at` (same-second ties resolve FIFO by seq —
  run order; second-precision timestamps cannot order events inside one wall-clock second).
- Resolution events: human accept/reject `review_actions`, no-diff terminals (window closed,
  no outcome), and un-overridden moa reject reviews. Reviews of an errored run's diff are
  not resolutions: `drainRun` journals partial diffs for failed runs, but the errored
  terminal alone closes that window, exactly like a no-diff terminal. A human review action
  for the same diff overrides a prior moa review; overridden moa reviews are neither
  outcomes nor boundaries.
- A skill is "in play" for an outcome when any non-slash `user_message` between the previous
  resolution and the resolved run's send carries the skill's path in its receipt (receipt
  paths under `.odo/skills/` — global and project scope share the marker). Slash messages
  journal no skill receipts and are excluded regardless.
- Outcome labels: human accept, human reject, moa weak reject (`consensus_verdict "reject"`
  without a subsequent human review action for that diff). `agent_error` runs never generate
  outcome labels — infrastructure noise is not skill signal; their terminals close the
  window whether or not a diff was journaled, so a failed run's receipts cannot bleed into
  the next outcome.

Reported per skill (grouped by receipt path, carrying the block hash of the newest
attributing window — the "last cohort"): injections, accepts, rejects, weak rejects,
accept/reject rates, skill-free baseline rates, and a deterministic flag.

Flag thresholds (ALL must hold):

| Leg | Threshold |
|---|---|
| injections | ≥ 10 resolved outcomes with the skill in play |
| human rejects | ≥ 3 |
| distinct conversations | ≥ 3 carrying those rejects |
| reject rate | ≥ 2× the skill-free baseline reject rate |

Weak rejects weight 0.5 in the rates; the rate comparison runs cross-multiplied in integers —
`(2R+W)*baseN >= factor*(2BR+BW)*inj` — so exactly-2× boundaries are not lost to float error;
an empty baseline means the rate leg is satisfied by any reject signal (already implied by
the rejects ≥ 3 leg). Usage: `odo skills audit [--json]`.

## O-1 — `odo autonomy audit` (RUNG-0 ONLY — no auto-apply ships)

`cmd_autonomy_audit.go` + `internal/ipc/autonomy.go`. Prints the autonomy streak snapshot:
per diff-class human-accept streaks, rung thresholds, and the `auto_apply` pref. The
computation is `ipc.ComputeAutonomy` — the SAME journal reads the `autonomy_status` IPC
serves the GUI — run against a read-only journal.

**Explicitly NOT shipped:** any auto-apply behavior. `CurrentRung` is hard-coded 0; nothing
applies, skips, or re-orders a review; the `auto_apply` pref is parsed and displayed, never
consumed. Rung-0 exists so a later milestone has evidence before any rung-1 behavior is
designed.

Diff classes (deterministic, from the journaled patch file, strict order C0→C1→C2→C3→""):

| Class | Definition |
|---|---|
| C0 | never-auto: protected path (`.odo/`, `wiki/`), >5 files, >300 changed lines, or new top-level dir |
| C1 | docs/wiki/comments only |
| C2 | tests only |
| C3 | small in-scope source: ≤3 files, ≤100 lines, every path previously accepted in the same workstream |
| "" | unclassified: anything else, or an unreadable patch |

Streak = consecutive human-accepted diffs of one class, zero human rejects, zero detected
reverts. Revert heuristic: a later accept resolving within 7 days, sharing ≥1 touched path,
mirroring ≥80% of the earlier accept's added/removed lines — the reset is CONSERVATIVE (the
class streak restarts from the reverting accept). Thresholds: 10 clean accepts → rung-1
eligibility; +20 more (30 total) → rung-2. Unreadable patch files are counted honestly, never
classified or revert-checked.

`auto_apply` pref contract (`internal/adapter/settings.go`): values `off|branch|main|all`,
default `off`. FAIL-CLOSED on unknown values — `ReadSettings` maps any non-listed value
(including absent) to `off`; `UpdateSettings` rejects an invalid value before writing a byte.
When a future rung consumes this pref, a typo must never silently widen apply scope. GUI:
DiffViewer chip (`DiffViewer.tsx`) shows `Auto-apply: <value>` + rung hint; read-only display.

## T4 — server concurrency: goroutine-per-connection VERIFIED (comments only)

`internal/ipc/server.go` already dispatches correctly; the M15 legs fixed the stale
"one connection at a time" comments across server.go, events.go, store.go, protocol.go,
App.tsx, and lib.rs (no behavior change, no test change). Evidence:

- Accept loop (server.go ~167-171): `s.wg.Add(1)` then
  `go func() { defer s.wg.Done(); s.handleConn(conn) }()` — one goroutine per connection.
- Drain (server.go ~179-181): `func (s *Server) Wait() { s.wg.Wait() }` — shutdown drains
  in-flight handler goroutines via the `wg sync.WaitGroup` field (~line 75, "active
  handleConn goroutines").
- Per-request fan-out is separate: review (~1479-1487) and panel (~1646-1660) handlers each
  use a local `var wg sync.WaitGroup` to run models in parallel inside their own connection.

## T2 — OMP memory injection probe: NOT OBSERVABLE (branch 2b, no receipts shipped)

Question: does OMP's `--mode json` stream (or session jsonl) carry the hindsight/snapcompact
injected memory block anywhere Odo can hash it? Answer: **no.** Injection is assembled
inside OMP before the first provider request and never crosses the stdout boundary. No fake
receipt was written; nothing feeds recall/distill/curate because nothing is captured.

Probe method (verbatim, 2026-08-10): scratch git repo under `/tmp`, spawned through the SAME
path the adapter uses — `internal/adapter/omp.go` `Start` →
`~/.hermes/profiles/orchestrator/scripts/omp_with_timeout.sh 600 <prompt> <out>
--hermes-provider custom:sudo --hermes-model t9s/kimi-k3 --task-tier normal
--session-dir <dir> --mode json` → wrapper line 1169: `omp -p "$(cat prompt)" --yolo
--max-time <s> <extra args> > out 2>&1 &` (omp v17.2.12). Prompt forced one tool call
(`ls -la`) so tool events would appear. Wrapper exit 0 in ~7s.

Event types enumerated from the captured stream (output.txt, 331 events):

    session×1 · agent_start×1 · turn_start×2 · message_start×4 · message_end×4 ·
    message_update×312 · tool_execution_start×1 · tool_execution_update×2 ·
    tool_execution_end×1 · turn_end×2 · agent_end×1
    message_start roles: user×1 (byte-identical to the prompt file — no preamble),
    assistant×2, toolResult×1

There is NO `system`/`memory`/`hindsight`/`snapcompact`/context event class in the stream.
Byte-grep for `hindsight|snapcompact` over output.txt and the run's session jsonl: 0 hits.
Session jsonl record types: `session`, `model_change`, `thinking_level_change`, `message`,
`custom` (only `tool_execution_start` + `session_exit`), `title` — no injected-context
record. A historical corpus scan (~all `~/.omp/agent/sessions/**/*.jsonl`) found "hindsight"
only inside assistant/toolResult text — sessions talking about hindsight, not injections.

Read-only toggle check (REPORT only — nothing changed):

- `omp --help` has NO memory/hindsight toggle; only `--no-skills`, `--no-rules`, `--advisor`,
  `--config=<overlay>` are adjacent.
- Binary schema (v17.2.12): `memory.backend` enum `off|local|hindsight|mnemopi`,
  **default `off`**; `hindsight.autoRecall`/`autoRetain` default `true` but are gated on
  backend; `autolearn.enabled` default `false`; warn path: "Hindsight: memory.backend=hindsight
  but hindsight.apiUrl is unset; backend inert."
- Local state: `~/.omp/agent/config.yml` sets NO `memory.backend` → `off`; apiUrl/apiToken
  absent → hindsight inert on this machine. `~/.omp/config.yaml` does not exist (the config
  file is `config.yml`). Compaction here is `strategy: context-full`; snapcompact is an
  alternate internal strategy that likewise emits no stream event.
- If anyone must disable hindsight for Odo-spawned runs, the only lever is the config key
  (`memory.backend: off`, already the default) or a `--config` overlay — a flag would be a
  defensive nicety, not a fix for an observed injection.

Caveat: the probe ran with `memory.backend=off`; a hindsight-active config was not available
to test. If a future OMP/config enables a memory backend or OMP adds a stream event for
context injection, re-run this probe and reassess 2a.

## Verify

    go build ./...
    go test ./...                      # incl. TestGetSettings (auto_apply default "off")
    cd gui && tsc --noEmit && vite build
    odo skills audit [--json]          # read-only report over the bound project
    odo autonomy audit [--json]        # rung-0 streak snapshot

## Data-gated next

- Flag-threshold tuning (S-1): the 10/3/3/2× constants are seeded from design, not data —
  revisit after ~2 weeks of receipts + resolutions accumulate.
- K3 downweight ladder: DEFERRED. Flag-only ships now; any automatic injection downweight
  needs the flag data first and a separate design round.
- T2 cutover trigger: revisit OMP-side memory accounting when EITHER (a) ~2 weeks of overlap
  data exist comparing Odo receipts vs OMP-internal context growth, OR (b) Odo-native
  semantic recall (M13's FTS5/bge-m3 spike) makes the OMP injection redundant. Until then the
  bytes are unhashable AND uninjected on this machine — both branches null.
