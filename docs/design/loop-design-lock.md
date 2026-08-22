# DESIGN LOCK — `/loop` (M19) — v1.1 — 2026-08-18

Tri-model proposal round: K3 / GLM-5.2 / DSF-v4-flash (blind, sealed, same
brief). Consolidation: orchestrator (no vote). Human amendments applied:
V6 land-each-round, V11 notification, V12 implementer pref, V13 slash
autocomplete. This file is the implementing contract; the milestone spec is
`docs/milestones/m19-loop.md` (committed first, invariant 7).

## Consensus base (C1–C12) — all three legs agreed; locked as-is

1. **Pure fold architecture**: `deriveLoopState(events) → loopState`, zero
   in-memory authority (ladderState doctrine), restart-proof by construction.
   Ticks fire at three seams only: `/loop` slash route, `drainRun` terminal
   tail (loop-provenance marker skips `maybeAutoLand`), audit-goroutine
   completion under `loopWG` (curateWG precedent, drained at `Wait()`). No
   supervisor goroutine.
2. **Single `/loop` grammar** with subcommands `audit | tasks | status |
   stop | resume`. Routed in `handleSendMessage` (the `/panel` prefix-block
   precedent); `/loop` goes into BOTH slash mirrors (`auditSlashCommands`,
   `rulesAuditSlashCommands`).
3. **Severity gate**: P0/P1/P2 block; P3/nits journaled, never hold the loop.
   `loop_hold_severity` pref can tighten to P1.
4. **Infra is never a verdict**: auditor timeout/transport/truncated/
   parse-error leg ⇒ round cannot be clean. One automatic round re-issue;
   second consecutive all-infra ⇒ `loop_suspended{audit_infra}`.
5. **Stall**: blocking-fingerprint set equal across two consecutive rounds
   (after an intervening fix) OR subject sha16 unchanged after a fix round ⇒
   `loop_suspended{stall}`. Never auto-resume.
6. **Closure blind spot (prompt-side)**: round R≥1 audit prompt carries R-1's
   journaled findings verbatim with a dual mandate: classify each
   `resolved|still_open|partially` AND "list ANY findings not named in the
   previous report — check the same code path the fixes touched for other
   behavior-changing controls."
7. **BYOF**: fix prompt embeds the actionable findings verbatim behind the
   settle demotion directive (quoted data, never instructions). Fixer sees no
   shared session context; auditors are fresh-context per round (direct API).
8. **Mode B inherits, never forks**: design = `runDesignMoa` (extracted from
   `handleDesignMoa`); review = `s.autoLand` verbatim (verify → panel →
   revise ladder ≤3 → majority-accept valve → suspension). Loop journals
   task-boundary rows only; it folds settle's terminal rows to learn
   outcomes.
9. **Mode A never reuses the settle ladder** — the audit→fix→audit loop IS
   the revise ladder. Loop round counters must not coexist with ladder
   counters on the same conversation.
10. **One active loop per conversation** (fold-derived; in-memory fast path
    `s.loops` is liveness-only, `s.designing` precedent). Admission is
    atomic (2026-08-22 P1 #6): the fold AND the `loop_started` append
    share one `s.mu` critical section in both start paths — concurrent
    /loop admissions settle exactly one winner.
11. **GUI-closed loops continue** (daemon-owned; chip re-derives on reopen).
    Esc/`/loop stop` terminal-cancels: in-flight fix run cancelled, fix
    diffs already landed stay landed (journaled).
12. **Budget + subject breakers**: `loop_budget_tokens` cumulative (charge =
    Σ journaled `output_tokens` + prompt chars/4 estimate at spawn,
    `spent_tokens` cumulative on each row), pre-spend projection over cap ⇒
    `loop_budget_exceeded` + suspend; `/loop resume budget=N` raises.
    Subject ≤ `loopAuditSubjectCapBytes` (256KB) — a loop-owned constant;
    findings feed stays ≤ `settleCommentsCapBytes` (16KB); over ⇒
    `loop_suspended{subject_too_large}` naming the cap and advising
    "land pending diffs first or narrow the base= range".

## Verdicts (V1–V13) — divergence points settled

| # | Decision |
|---|---|
| V1 | **Journal = one discriminated type** `store.EventLoopEvent = "loop_event"`, discriminated by `kind`. NOT 7 new types, NOT additive junk inside `review_action`/`memory_update`. One consumer touch-point (types.ts union + one bubble case + fold filters). |
| V2 | **No driver goroutine per loop**; ticks at the three seams (C1). `loopWG` exists only to keep blocking MoA fan-outs off the IPC thread. |
| V3 | **Fingerprint is engine-computed**, `sha16(norm(file)\|norm(symbol)\|norm(title))` — line and severity EXCLUDED (line drifts; severity max-wins union). No consolidator model for audit findings; union is mechanical, deterministic, falsifiable. Audit leg emits a fenced findings block `− sev: PX \| file: … \| symbol: … \| title: …`; garbage ⇒ `parse_error`, contributes nothing, round cannot close clean. |
| V4 | **Budget axes = rounds cap + cumulative tokens + subject bytes** (C12). NO per-round token cap (noisy, dominated by subject size). |
| V5 | **Protected paths**: any land attempt touching protected paths (`.odo/`, `wiki/`, memory layers) or the risk-classifier `supply_chain` class ⇒ `loop_suspended{risk:<class>}` + risk receipt on the round row. In land-each-round mode a protected-path fix cannot be absorbed — suspend is the only coherent state; human lands manually then resumes. |
| V6 | **Mode A lands each round to main** (user amendment, overrides K3's accumulate-mode). Subject = `git diff base..HEAD` with base frozen at loop start; HEAD advances as fixes land. Sequence per round: audit legs → findings → fix run → `runVerifyGate()` (extracted from `autoLand`, called verbatim) → risk gate → journaled autoActor land. **SEED phase**: if pending diffs exist at `/loop audit` start, drive each through `s.autoLand` verbatim first (blocked ⇒ suspend); base = HEAD before seed. If after seed/none the subject is empty and no `base=` arg ⇒ pre-journal error `nothing_to_audit`. Verify failure ⇒ no land; journaled as round fact and shown to the next audit prompt (advisory; it is NOT a land-blocking suspend). Loop granularity stays small-fix; recurring heavy findings trip stall (C5) → user moves them to Mode B. |
| V7 | **Restart recovery**: `recoverLoops()` in `NewServer` (`recover-pending-diffs`/`recoverParkedGoals` precedent). Mid-audit at kill (no side effects) ⇒ idempotent re-run, `loop_recovered{reran_audit}`. Mid fix/implement run ⇒ `loop_suspended{restart_mid_run}` (worktree may hold partial side effects; human resumes). |
| V8 | **Human interleave**: a `send_message` without loop-provenance marker while a loop is active ⇒ `loop_suspended{human_interleave}` (deterministic; the conversation never refuses the send). `/loop resume` refolds and re-ticks. Parked-goal auto-dequeue is suppressed while a loop is active (guard in the dequeue fold). |
| V9 | **Mode B task sources**: inline numbered list · `file:<project-rel .md>` (containment same as `handleReadFile`; `..` refused) · `queue` (drains parked FIFO, `run_prompt{goal_seqs}` receipts per task). Todo-store source is NOT v1. |
| V10 | **Milestone spec committed before code** (this pair of docs). |
| V11 | **`loop_notify_on_complete` pref (default on)**: GUI derive watches terminal kinds (`loop_completed|loop_stopped|loop_suspended|loop_budget_exceeded`); first sight of a terminal row for a loop_id fires ONE Tauri system notification (`@tauri-apps/plugin-notification`) and journals `loop_notified` (prevents re-fire on reopen). Daemon never touches OS services; firing requires GUI open — honest limit, no workaround. |
| V12 | **`loop_implementer` pref (default = `coding:` line)**: spawn-time model override for loop fix/implement runs; informational in `README`/Settings. Does NOT change normal `send_message` resolution. |
| V13 | **Slash autocomplete UX (Hermes-parity)**: typing `/` opens the full command list immediately (unfiltered at first keystroke, filter as you type), one-line description per entry, ↑/↓ + Tab/Enter accept, Esc closes, accepted command renders as a highlighted token inside the input (overlay approach over the existing textarea). Reuses existing `SLASH_COMMANDS` registry; adds description field per entry. |

## Command surface (final)

```
/loop audit [base=<sha>] [rounds=N] [budget=T]      Mode A; SEED lands pending diffs first
/loop tasks [rounds=N] [budget=T] <inline 1.… 2.… | file:<md> | queue>
/loop status                                        fold dump into chat
/loop stop                                          terminal; cancels in-flight run
/loop resume [budget=T]                             clear suspendable causes, re-tick
```

Flags parse only from the LEADING run of key=value tokens, never from
task text (post-lock P2 amendment: a `k=v` token inside a task is inert,
and leading flags never pollute task 1's text).

GUI-only IPC: `CmdLoopCtl` `{action: approve_design | amend_design(text) |
veto_design | stop | resume(budget)}` (Mode B design gate + chip buttons).

## prefs (fail-to-default, read per-tick)

| key | default | note |
|---|---|---|
| `loop_max_rounds` | 10 | ≥1 |
| `loop_budget_tokens` | 2000000 | ≥100000 |
| `loop_hold_severity` | P2 | P1\|P2 else log+P2 |
| `loop_auditor_models` | `review:` line | `parseReviewModels` |
| `loop_implementer` | `coding:` line | V12 spawn override |
| `loop_design_gate` | human | `auto` skips per-task DESIGN LOCK veto |
| `loop_consolidator` | `orchestrator:` | design consolidator |
| `loop_notify_on_complete` | on | V11 |

## loop_event kinds (journal)

`loop_started, loop_design_lock, loop_task_spawn, loop_task_done,
loop_audit_round, loop_verdict, loop_fix_spawn, loop_diff_bound,
loop_suspended, loop_completed, loop_stopped, loop_budget_exceeded,
loop_recovered, loop_resumed, loop_notified`. Common keys: `kind, loop_id (seq of
loop_started), mode (audit|tasks|tasks_final), actor:"auto_loop",
spent_tokens (cumulative)`. P1 #13 (2026-08-22): `loop_diff_bound{diff_id,
round?|task?}` journals at DRAIN when a loop-bound run's diff is inserted —
the loop⇄diff binding (the spawn row's old `diff_id?` contract was dead:
the diff does not exist at spawn). `loopAdjudicateTask` attributes
accept/blocked rows to a task ONLY through this chain (pre-binding
journals keep the pipeline-actor fallback); recoverPendingDiffs skips
non-terminal loops' bound + seed diffs (the boot double-panel twin). Per-leg wire receipts riding audit/design rows:
`model, verdict (complete|parse_error|infra|truncated), findings_count,
request_sha16, request_bytes, output_tokens, escalations, base_url_scrubbed`.
Fix prompts journal as verbatim `user_message` rows with marker
`loop_fix{loop_id, round|task}` (auto_revise precedent + distill
tombstone). Oversize bodies (>32KB) go to `.odo/loop/<id>/` artifact files;
journal carries sha16+path only. `loop_verdict` per audit round:
`verdict: clean|fix|audit_infra|stall|round_cap` + `blocking_fps, new_fps,
carried_fps` + reason — the stop decision, re-derivable from the journal
alone.

## Failure matrix (headline policies)

no-diff fix run ⇒ suspend `fix_no_diff` (one automatic re-spawn on resume);
tainted run (no_text/false_stop) ⇒ suspend `run_tainted`; auditor infra ⇒
round invalid, never clean (one re-issue, then suspend); payment/grievance
mid-round ⇒ surfaces as infra legs; GUI closed ⇒ irrelevant; daemon kill ⇒
V7; circular findings ⇒ stall (C5); supply-chain/protected ⇒ suspend
`risk:<class>`; subject >64KB ⇒ suspend `subject_too_large`; human send ⇒
`human_interleave`; design consolidator truncation ⇒ fail-closed suspend
`design_infra`; second `/loop` while active ⇒ pre-journal refusal; parked
dequeue during loop ⇒ suppressed.

## Test plan (settleRig parity, ~26 Go + E2E + vitest)

Rig: `loopRig` = `startPanelStub`-style scripted gateway (audit legs emit
findings blocks; design legs + consolidator multiplex per model, the
`design_moa_test` shape) + `settleRigRepo` fixture + `ODO_OMP_WRAPPER` stub
emitting scripted diffs + `scanLoop` fold asserts + `loopWG.Wait()` joins.
Key pins: fixpoint 3-rounds-clean; P3 never spawns a fix round; infra/
truncated/parse-error leg blocks clean; stall on set-equality and on
subject-sha-unchanged; round cap; budget overflow + `resume budget=`;
human interleave suspend; stop terminal; restart mid-audit re-run vs
mid-run suspend; second loop refused; supply-chain + protected suspend;
ComputeAutonomy regression (loop rows invisible to autonomy); Mode B happy
path (2 tasks → design locks with wire receipts → autoLand passthrough →
final audit clean); design veto; panel_mixed passthrough suspend; revise
ladder settle-rows-identity (no loop duplication); queue source FIFO +
goal_seqs receipts; preflight `auto_apply!=main` refusal for tasks;
file-source containment refusal; parked-dequeue suppression quiet window;
SEED phase lands pending union before round 1; V6 per-round land + subject
= `git diff base..HEAD` per round; notification single-fire + `loop_notified`
journal. E2E: slash autocomplete behavior (immediate full list, descriptions,
keyboard nav, chip highlight), chip renders phase/round/spent, stop works,
notification mocked-fire on terminal kind. vitest: `deriveLoopStates` table.

## Implementation notes

Extractions (mechanical, regression-witnessed by existing tests staying
green): `runDesignMoa(ctx, goal, files)` out of `handleDesignMoa`;
`runVerifyGate(ctx, wtPath)` out of `autoLand`. Tauri notification plugin
added to `gui/src-tauri` + capabilities. New files (rough):
`internal/ipc/loop.go` (~550) + `loop_audit.go` (~300) +
`loop_journal.go` (~200) + tests (~1350) + `gui/src/loop.ts` (~150) +
`LoopChip.tsx` (~120) + autocomplete upgrade in `ChatSurface.tsx` +
`gui/e2e/loop.spec.ts`. Touched: store.go (+1 const), server.go (+~45),
protocol.go (+~15), autoland.go (+10/−20 extract), design_moa.go
(+20/−30 extract), parked.go (+8), slash mirrors (+2), types.ts/StatusBar/
api.ts, README.
