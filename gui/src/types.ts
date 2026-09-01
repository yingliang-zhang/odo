// Shared types mirroring the daemon's IPC shapes (internal/ipc/protocol.go)
// and store structs (internal/store/store.go). JSON field names match the Go
// struct tags exactly — in particular, Event payloads ride under `payload`
// as a nested JSON object, NOT a stringified `payload_json`.

export interface Project {
  id: number;
  name: string;
  root_path: string;
  created_at?: string;
}

// M11 P1: one row of the daemon-owned global registry
// (~/.odo/projects.json) for the project switcher — read-only bridge view
// via list_projects. Distinct from Project, which is the daemon's DB row.
export interface ProjectEntry {
  root: string;
  name: string;
  added: string;
}

export interface Workstream {
  id: number;
  project_id: number;
  name: string;
  branch?: string | null;
  worktree_path?: string | null;
  status: string;
  created_at?: string;
}

export interface Conversation {
  id: number;
  workstream_id: number;
  epoch: number;
  state: string;
  base_commit_sha?: string | null;
  created_at?: string;
}

export type EventType =
  | "user_message"
  | "agent_text"
  | "agent_thinking"
  | "agent_tool_call"
  | "agent_tool_result"
  | "agent_done"
  | "agent_error"
  | "review_action"
  | "memory_update"
  | "preview_captured"
  | "loop_event"
  | (string & {});

// Payload keys by event type (ADR-0002):
//   user_message      { text, attachments? }
//   agent_text        { text }
//   agent_tool_call   { tool, args }
//   agent_tool_result { tool, result }
//   agent_done        { summary }
//   agent_error       { error, odo? } — odo:true marks a journaled DAEMON
//                       advisory (journalRunAdvisory), never a run failure;
//                       UX-3a (A2-6a) finally consumes the flag: failure
//                       notifications and the finished-flash ✗ tint skip it.
//   review_action     { action: "accept" | "reject", diff_id }
//                       — also "distill" { epoch, wiki_path } (M1; plus
//                       first_seq, last_seq, note_sha — explicit fold
//                       provenance) and "curate" { action, topics,
//                       notes_read } (M5).
//   memory_update     { layer, cause, before_sha?, after_sha?, detail? }
//                       layer: "memory" | "user" | "learner" (M4) |
//                       "curator" | "index" | "pins" (M5) |
//                       "note" | "ledger" (M6); cause:
//                       "apply" | "rotate" | "retract" | "failed" (M4) |
//                       "curate" | "pin" (M5) |
//                       "write_failed" | "verify_failed" (M6).
//   preview_captured  { url, bytes, sha256, wait_ms } — /preview's
//                       headless-capture receipt, journaled right after
//                       its slash user_message. sha256 is the full hex of
//                       the PNG; the user_message's image_sha16 covers
//                       the same bytes as the wire preimage.
//   loop_event        M19 (/loop) — ONE discriminated type per design
//                       lock V1: kind ∈ loop_started | loop_design_lock |
//                       loop_task_spawn | loop_task_done |
//                       loop_audit_round | loop_verdict | loop_fix_spawn |
//                       loop_suspended | loop_completed | loop_stopped |
//                       loop_budget_exceeded | loop_recovered |
//                       loop_resumed | loop_notified. Common keys: kind,
//                       loop_id (seq of loop_started — the started row
//                       itself carries loop_id 0 and IS the id), mode
//                       (audit|tasks|tasks_final), actor "auto_loop" (or
//                       origin "loop_ctl" on GUI rows), spent_tokens
//                       (cumulative). Per-kind extras ride the daemon's
//                       journal contract (internal/ipc/loop_journal.go);
//                       the fold below consumes: max_rounds,
//                       budget_tokens, base, tasks, task, status,
//                       verdict, cause, terminal_kind, round,
//                       subject_sha16.
// M6: one recall payload entry. Fixed markers (user.md/memory.md/pins.md/
// index.md) carry path only; keyword-selected notes carry matched_terms
// (omitted when the note ranked in purely by newest-first fallback).
export interface RecallItem {
  path: string;
  matched_terms?: string[];
}

export interface EventPayload {
  text?: string;
  summary?: string;
  error?: string;
  tool?: string;
  args?: unknown;
  result?: unknown;
  action?: string;
  diff_id?: number;
  attachments?: string[];
  // W6 (goal queue): user_message carries park:true when the user queued
  // the goal for later (derives the durable parked-goal FIFO);
  // review_action rows consume queued seqs — run_prompt{goal_seqs} marks
  // dequeuing runs (origin "parked_goal", actor set for automatic
  // dequeues, absent for a human resume), parked_goal_dropped{goal_seq}
  // journals a human drop.
  park?: boolean;
  goal_seqs?: number[];
  goal_seq?: number;
  actor?: string;
  // Steer queue (Hermes-style busy run): user_message carries steer:true
  // when the message journaled as a mid-run instruction; review_action
  // rows close the ledger — run_prompt{steer_seqs} marks the
  // continuation/retry run that consumed the drained steers,
  // steer_dropped{steer_seqs|steer_seq, cause?} journals abandonment
  // (drain batch, with cause) or a human drop (single seq, no cause).
  steer?: boolean;
  steer_seqs?: number[];
  steer_seq?: number;
  // M3: memory recall — user_message journals what was injected into the
  // prompt. Fixed markers come first in daemon order: ~/.odo/user.md →
  // .odo/memory.md (M4) → .odo/pins.md (M5) → wiki/index.md (M5), then the
  // recalled wiki notes. M6 shape change: string[] → RecallItem[]; pre-M6
  // events still carry bare strings, so consumers normalize both shapes
  // (spec risk #4).
  recall?: RecallItem[];
  // M4: injection receipt — content hashes (sha256[:16]) of the exact
  // blocks injected, keyed by the same path strings used in `recall` (M5
  // adds the .odo/pins.md and wiki/index.md fixed markers).
  receipt?: Record<string, string>;
  // M18 W2 (prompt receipt closure): run-starting paths journal the
  // assembled prompt's byte total + sha16, plus the recall cap's held-back
  // count when >0, and the replay structural sub-receipt when a replay was
  // injected. Rides user_message on send/slash and
  // review_action{action:"run_prompt"} on continuations — the GUI's
  // context-pressure meter reads the latest of these.
  total_prompt_bytes?: number;
  prompt_sha16?: string;
  recall_held_back?: number;
  replay?: {
    after_seq: number;
    first_seq: number;
    last_seq: number;
    bytes: number;
    dropped_seqs?: number[];
  };
  // GUI Wave B (defensive): billed usage on agent_done. NOTHING writes
  // these yet — the OMP adapter drops the stream's usage at the seam, so
  // the stats strip derives sizes from journaled bytes and only upgrades
  // to billed tokens when a payload carries them (timed_out precedent).
  input_tokens?: number;
  output_tokens?: number;
  // memory_update payload fields (M4, extended M5): which layer changed
  // (memory | user | learner | curator | index | pins | auto_distill —
  // the scheduler journals layer:"auto_distill" decision rows), why
  // (apply | rotate | retract | failed | curate | pin | scheduled |
  // fired | skipped | cancelled_by_send), and a human-readable summary.
  layer?: string;
  cause?: string;
  detail?: string;
  // review_action when action == "distill" (M1 memory distiller). The fold
  // window [first_seq, last_seq] is journaled explicitly (epoch-fold root
  // fix) alongside the note's sha16; older markers omit them and consumers
  // derive (previous marker seq+1 … marker seq−1).
  epoch?: number;
  wiki_path?: string;
  first_seq?: number;
  last_seq?: number;
  note_sha?: string;
  // review_action when action == "curate" (M5 curator pass): how many
  // topic pages were rewritten and how many epoch notes were read.
  topics?: number;
  notes_read?: number;
  // review_action when action == "auto_land_started" (auto-land indicator
  // lock Phase 2): which silent stage the pipeline just entered —
  // "verify" (the .odo-verify gate) or "panel" (the review fan-out). The
  // pipeline chip derives its "running" label from this.
  stage?: string;
  // fix-INT W5 (Guardian risk taxonomy, internal/ipc/risk.go): receipt-
  // eligible review_action rows (accept/reject/auto_land_blocked/moa_review/
  // auto_revise_round) carry a PURE-MECHANICAL risk receipt —
  // risk_class is severity-ranked multi-label (["none"] = explicitly rated
  // clean); the key is ABSENT on pre-W5 rows (unrated, never a false clean)
  // and when the patch was unreadable. risk_evidence (one trigger artifact
  // per class) is omitted on clean rows; risk_classifier is the provenance
  // constant ("mechanical" this wave).
  risk_class?: string[];
  risk_evidence?: Record<string, string>;
  risk_classifier?: string;
  // Review-decision result fields ridden by the auto-land pipeline (M16 +
  // settle ladder): consensus_verdict on moa_review; reason on
  // auto_land_blocked; round on auto_revise_round; outcome
  // (clean|conflict|error) + phase (pre_spend_probe|accept_apply) on
  // refresh_attempted (P0a — no risk receipt, it's a rebase, not a verdict).
  consensus_verdict?: string;
  reason?: string;
  round?: number;
  outcome?: string;
  phase?: string;
  // M19 (/loop): loop_event discriminated-type keys (V1 — the single
  // consumer touch-point). `loop_id` is the loop_started row's seq on
  // every later row; the started row itself carries 0. Fix/implement
  // lands and verify/land failures do NOT ride this type — they stay
  // review_action{accept | auto_land_blocked{reason:"loop_*"},
  // actor:"auto_loop"} rows, attributing to the newest non-terminal loop
  // (the Go fold's rule, mirrored in loop.ts).
  kind?: string;
  loop_id?: number;
  mode?: string; // audit | tasks | tasks_final
  spent_tokens?: number; // cumulative ledger at row time
  max_rounds?: number;
  budget_tokens?: number;
  base?: string;
  tasks?: string[]; // loop_started's Mode B task list
  task?: number; // loop_design_lock / loop_task_spawn / loop_task_done
  status?: string; // loop_task_done: landed|settle_blocked|vetoed|design_infra|skipped
  verdict?: string; // loop_verdict: clean|fix|audit_infra|stall|round_cap
  subject_sha16?: string; // loop_audit_round's audited diff subject
  terminal_kind?: string; // loop_notified's receipt key
  rounds?: number; // loop_completed's total audited rounds
  fixes_landed?: number; // loop_completed summary
  findings_count?: number; // loop_audit_round / loop_fix_spawn actionable findings
  budget?: number; // loop_resumed's optional budget raise
  prompt_tokens_est?: number; // spawn-time prompt estimate (chars/4)
  // D3 loop_run_usage receipt (internal/ipc/loop_journal.go contract):
  // the drain's measured executor cost. covers_spawn_seq pins the spawn
  // row whose estimate the fold retires (0 ⇒ match by round/task);
  // usage_available:false + reason is the fail-soft row (estimate stays
  // pending). cache_read is journaled, never budgeted.
  kind_run?: string;
  run_id?: string;
  covers_spawn_seq?: number;
  usage_available?: boolean; // (reason? rides the shared review_action key above)
  cache_read_tokens?: number;
  cache_write_tokens?: number;
  cost_usd?: number;
  // Read-only run/verify log (tri-model right sidebar gap): moa_review rows
  // from the auto-land pipeline carry the verify that attested the landing —
  // the command and its capped output tail (previously prompt-ephemeral
  // only). Blocked rows already carry theirs inside `detail`.
  verify_cmd?: string;
  verify_tail?: string;
  // auto_revise_round chains the row to the diff being repaired
  // (settle.go:598): round 1 carries diff_id == origin_diff_id (chain
  // start), later rounds carry the just-evaluated product's id with the
  // SAME origin — the GUI resolves chain membership from this pair.
  origin_diff_id?: number;
  // auto_revise_product (drainRun Fix B1): chain bookkeeping linking the
  // repair product back to its origin — {product_diff_id, origin_diff_id},
  // no diff_id. Derivation skips it (pipeline.ts); supersedeChain consumes
  // it daemon-side on land.
  product_diff_id?: number;
  // Timed-out review marker (codex ReviewDecision.TimedOut parity):
  // defensive — rendered when the payload carries it, nothing writes it yet.
  timed_out?: boolean;
  // M12 (D-todo): review_action when action == "todo_merge" — the full
  // post-merge live snapshot (open + not-yet-swept items), its origin, and
  // op bookkeeping. Derived stale/swept flags are recomputed by consumers
  // from the fold markers, never journaled.
  origin?: string;
  ops_applied?: number;
  ops_rejected?: { op?: string; id?: string; reason: string }[];
  snapshot?: { id: string; text: string; status: string; origin_seq: number; updated_seq: number }[];
  snapshot_sha?: string;
  // M7: streaming-preview payloads. partial marks the transient preview
  // (never journaled); intent/call_id come from tool_execution_start.
  partial?: boolean;
  intent?: string;
  call_id?: string;
  // /panel and /vision payloads: mark the event as panel/vision output and
  // carry per-model results for /panel; /preview answers ride the same
  // vision shape (plus preview:true provenance).
  panel?: boolean;
  vision?: boolean;
  preview?: boolean;
  models?: { model: string; text: string; error?: string }[];
  // agent_error provenance flag: daemon advisories (journalRunAdvisory)
  // journal with odo:true so consumers can tell "the daemon says" from
  // "the run failed" (UX-3a consumes the flag; UX-3b restyles the chips).
  odo?: boolean;
  // preview_captured payload fields: the captured URL, PNG byte size,
  // full-sha256 audit hash, and the per-shot wall time.
  url?: string;
  bytes?: number;
  sha256?: string;
  wait_ms?: number;
}

// /panel heartbeat: the daemon's in-memory fan-out tally for the polled
// conversation (never journaled — the previewEvent precedent). done legs
// have answered of the total batch; drives the panel spinner row's N/M
// counter during multi-minute consults. Absent when no panel is in flight.
export interface PanelProgress {
  done: number;
  total: number;
  // Per-leg rows registered at fan-out (prefs order): the status card
  // shows who is still out, who is back, and who errored. Absent on rows
  // from a daemon predating leg detail.
  legs?: PanelLeg[];
}

// One model's slot in a /panel fan-out: done flips on any answer, error
// distinguishes a failure leg from a successful one.
export interface PanelLeg {
  model: string;
  done: boolean;
  error?: boolean;
}

// M7 live streaming: the transient in-flight block preview returned by
// poll_events while a run streams. Never journaled — replaced wholesale on
// every poll and dropped when the block completes.
export interface PreviewEvent {
  type: EventType;
  payload: EventPayload;
}

export interface OdoEvent {
  id: number;
  conversation_id: number;
  seq: number;
  type: EventType;
  payload: EventPayload;
  created_at: string;
}

export type DiffStatus = "pending" | "accepted" | "rejected" | (string & {});

export interface Diff {
  id: number;
  status: DiffStatus;
  path: string;
  content: string;
}

// P1a (review inbox): one inbox row — the daemon's DiffInfoEx embeds
// DiffInfo, and Go flattens the embedded struct, so the wire shape is a
// single object carrying both the diff fields and the workstream label.
export interface DiffInfoEx extends Diff {
  workstream_name: string;
  conversation_id: number;
  workstream_id: number;
}

export interface ListAllPendingDiffsResponse {
  ok: boolean;
  error?: string;
  all_pending_diffs?: DiffInfoEx[];
}

export interface BootstrapResponse {
  ok: boolean;
  error?: string;
  project?: Project;
  workstream?: Workstream;
  conversation?: Conversation;
  events?: OdoEvent[];
  agent_running?: boolean;
  diff?: Diff | null;
}

// Type alias (not interface) so it is assignable to Tauri's InvokeArgs.
export type SendMessageRequest = {
  conversationId: number;
  text: string;
  attachments?: string[];
  // M1: steer journals the message for a running agent instead of starting
  // a new run; adapter selects the backend ("omp").
  steer?: boolean;
  adapter?: string;
  // W6 (goal queue): park queues the message as a parked goal instead of
  // sending/steering (the daemon refuses steer+park, so the composer
  // passes at most one).
  park?: boolean;
  // M11 P1: routes to that project's daemon; null = bridge default.
  projectRoot?: string | null;
};

export interface SendMessageResponse {
  ok: boolean;
  error?: string;
  event?: OdoEvent;
  // W6: the conversation's parked-goal queue depth after a park.
  parked?: number;
}

// W6 (goal queue): one queued goal, derived from the journal
// (gui/src/parked.ts mirrors the daemon's deriveParkedGoals): the seq of
// its user_message{park:true} row plus the verbatim text.
export interface ParkedGoal {
  seq: number;
  text: string;
}

// Steer queue: one queued mid-run instruction, derived from the journal
// (gui/src/steer_queue.ts mirrors the daemon's drain ledger): the seq of
// its user_message{steer:true} row plus the verbatim text.
export interface QueuedSteer {
  seq: number;
  text: string;
}

// Daemon `resume_parked_goal` / `drop_parked_goal` response. ok:false
// (e.g. "no parked goal with seq N") is a benign reconcile — an
// auto-dequeue raced the click, and the next poll reflects it.
export interface ParkedGoalResponse {
  ok: boolean;
  error?: string;
  parked?: number;
}

// Daemon `drop_queued_steer` response (mirrors ParkedGoalResponse's
// envelope, minus the queue depth — the steer queue is journal-derived
// only and rides no pending_counts field). ok:false ("no queued steer
// with seq N") is a benign reconcile — the drain consumed the steer
// first, and the next poll reflects it.
export interface QueuedSteerResponse {
  ok: boolean;
  error?: string;
}

// Belt A: cancel carries no payload; ok:false ("no active run") is the
// normal race against a run that finished just before the click.
export interface CancelResponse {
  ok: boolean;
  error?: string;
}

// M19 (/loop): daemon `loop_ctl` — design gate + chip stop/resume +
// notification receipts. Stop/resume journal their row and carry it back;
// notified is idempotent (a journaled receipt short-circuits to ok with
// no event). ok:false names the fold-level refusal ("no active loop",
// "not suspended").
export interface LoopCtlResponse {
  ok: boolean;
  error?: string;
  event?: OdoEvent;
}

export interface PollEventsResponse {
  ok: boolean;
  error?: string;
  events?: OdoEvent[];
  agent_running?: boolean;
  // M7: transient in-flight block preview (partial:true), or null.
  preview?: PreviewEvent | null;
  streaming?: boolean;
  // /panel heartbeat: live fan-out tally for this conversation, or null.
  panel_progress?: PanelProgress | null;
  diff?: Diff | null;
  diffs?: Diff[];
}

export interface AcceptDiffResponse {
  ok: boolean;
  error?: string;
  diff_id?: number;
  applied?: boolean;
}

export interface RejectDiffResponse {
  ok: boolean;
  error?: string;
  diff_id?: number;
  applied?: boolean;
}

export interface ListWorkstreamsResponse {
  ok: boolean;
  error?: string;
  workstreams?: Workstream[];
}

export interface CreateWorkstreamResponse {
  ok: boolean;
  error?: string;
  workstream?: Workstream;
}

export interface DistillResponse {
  ok: boolean;
  error?: string;
  wiki_path?: string;
  // epoch is the NEW epoch after the increment (the distilled one is N-1).
  epoch?: number;
}

// ---------- M2: review panel, settings ----------

export type ReviewVerdict = "accept" | "reject" | "needs_fixes" | (string & {});

// One reviewer's verdict on a pending diff (daemon `review_diff`).
export interface ReviewResult {
  model: string;
  verdict: ReviewVerdict;
  comments: string;
}

export interface ReviewDiffResponse {
  ok: boolean;
  error?: string;
  reviews?: ReviewResult[];
  consensus?: string; // A4-lite+v2: deterministic verdict ("accept" | "reject" | "needs_fixes"); accept requires unanimity
}

// ---------- M15 (O-1 rung-0): autonomy streak snapshot ----------

// One diff-class row of the autonomy report (daemon `autonomy_status`).
export interface AutonomyClassReport {
  class: string; // "C0" | "C1" | "C2" | "C3" | "unclassified"
  description: string;
  accepted: number;
  rejected: number;
  streak: number;
  next_threshold: number; // 0 = none (terminal / non-rung class)
  eligible: string; // "" | "rung-1" | "rung-2" (eligibility math only — rung 0 applies nothing)
}

// Rung-0 observability snapshot. The auto_apply pref is displayed, never
// consumed — no auto-apply behavior exists.
export interface AutonomyReport {
  project_root: string;
  journal: string;
  workstreams_scanned: number;
  conversations_scanned: number;
  resolutions: number;
  unreadable_diffs: number;
  auto_apply: string;
  current_rung: number; // always 0 today
  rung_thresholds: Record<string, number>;
  revert_check: string;
  classes: AutonomyClassReport[];
}

export interface AutonomyStatusResponse {
  ok: boolean;
  error?: string;
  autonomy?: AutonomyReport;
}
// ---------- D9-W3: learning control plane (pure observability) ----------

// The 16 episode outcome keys, exactly as the daemon's `learning_status`
// fold always emits them (zero-filled, never key-elided — the explicit
// fields pin that). `episode_totals` reuses this shape: the same keys
// summed over ALL journal episodes, not just the returned window.
export interface LearningOutcomeRow {
  accepted: number;
  rejected: number;
  weak_rejected: number;
  auto_accepted: number;
  auto_rejected: number;
  verify_failed: number;
  panel_mixed: number;
  panel_minority_reject: number;
  revise_rounds_spawned: number;
  revise_landed: number;
  ladder_suspended: number;
  revise_no_progress: number;
  agent_errors: number;
  false_stops: number;
  no_texts: number;
  human_reverts: number;
}

// Outcome-context counters. panel_infra/blocked_other/diff_less_terminals/
// attribution_lost share the outcomes' always-emitted guarantee (zeroed);
// attribution_lost = raw human outcomes whose send/terminal predates the
// episode window (honest window-boundary reconciliation).
// memory_free_outcomes (attributed outcomes with no memory block in play)
// is emitted ONLY when >0, hence optional.
export interface LearningContextRow {
  panel_infra: number;
  blocked_other: number;
  diff_less_terminals: number;
  attribution_lost: number;
  memory_free_outcomes?: number;
}

// Token/cost usage of the episode's distill call; available=false means
// the harness reported no usage block (the numeric fields are zeroes).
export interface LearningUsageRow {
  available: boolean;
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  cost_usd: number;
}

// One journaled learning episode (per-distill outcome accounting over a
// journal window). `episodes` arrives newest-first, capped at 50.
export interface LearningEpisodeRow {
  seq: number;
  conversation_id: number;
  workstream: string;
  epoch: number;
  window: { first_seq: number; last_seq: number };
  outcomes: LearningOutcomeRow;
  context: LearningContextRow;
  flags_emitted: number[];
  usage: LearningUsageRow;
  verify_ms_total: number;
  distill_ms: number;
}

// One flagged rule from the rules audit fold (D9-W3 is the FIRST flag
// surface — MemoryPanel never landed one). verdict is "harmful" (reject
// pressure past thresholds) or "effective".
export interface LearningFlagRow {
  seq: number;
  rule: string;
  verdict: "harmful" | "effective" | (string & {});
  injections: number;
  rejects: number;
  reject_conversations: number;
}

// The audit thresholds the flags cleared — surfaced verbatim so the GUI
// never re-derives the gate.
export interface LearningFlagThresholds {
  min_injections: number;
  min_rejects: number;
  min_reject_conversations: number;
  rate_factor: number;
}

// One candidate-lifecycle row. W3 ships an EMPTY list (nothing writes
// candidates yet); the shape locks now so W4's writer needs no GUI change.
export interface LearningCandidateRow {
  artifact_hash: string; // 64-hex content address; GUI shows the first 12
  version: number;
  scope: string; // e.g. "project:memory"
  stage: string; // candidate|shadow|canary|project_active|...
  created_seq: number;
  created_at: string; // RFC 3339
  invalid: boolean; // hash-chain unresolvable ⇒ fold marks invalid, refuses transitions
  // D9-W6: daemon-folded stall marker — a learning_stall advisory exists
  // for this candidate (advisory only; never auto-promoted/auto-dropped).
  stalled?: boolean;
}

// D9-W6: one journaled learning_stall advisory (W5 emits the row, W6
// surfaces it). Rendered in the Candidates stage feed — no new panel.
export interface LearningStallRow {
  seq: number;
  conversation_id: number;
  artifact_hash: string;
  stage: string; // the stage the candidate aged in
  epoch: number; // main-lane epoch the advisory fired
  reason: string;
}

// Daemon-folded learning report (single fold; the GUI renders, never
// re-folds — dual-fold skew is a locked failure mode).
export interface LearningStatus {
  project_root: string;
  journal: string;
  episodes: LearningEpisodeRow[];
  episode_count: number;
  episode_totals: LearningOutcomeRow;
  flags: LearningFlagRow[];
  flag_thresholds: LearningFlagThresholds;
  candidates: LearningCandidateRow[];
  stalls: LearningStallRow[]; // D9-W6, newest-first per seq
}

export interface LearningStatusResponse {
  ok: boolean;
  error?: string;
  learning?: LearningStatus;
}

// Daemon-managed project settings (daemon `get_settings` / `update_settings`).
export interface Settings {
  coding_model: string;
  coding_provider: string;
  orchestrator_model: string;
  orchestrator_provider: string;
  omp_timeout: string;
  review_models: string;
  // M12: default flipped to "on_idle"; the auto_curate_after_distill pref
  // is gone (auto-curate is daemon-conditional now).
  auto_distill: string;
  auto_distill_idle_seconds: string;
  max_concurrent_runs: string;
  // M15 (O-1 rung-0): off|branch|main|all — parsed and displayed only.
  auto_apply: string;
  // M19 (V11): daemon's loop_notify_on_complete pref, resolved
  // fail-to-default ON (an explicit off-shape pref value reports false).
  // Read-only over IPC — hand-edited in prefs.md. Optional because a
  // pre-M19 daemon never sends it; absent reads as ON by the watcher.
  loop_notify_on_complete?: boolean;
  // UX-2 (D5 Stage 0 / A2-3): k8s observability prefs. Always sent
  // (Settings has no omitempty on the Go side); "" namespace = feature
  // OFF — the k8s_status handler answers reason:"off" without exec'ing.
  // Writable over IPC (settings writes round-trip through update_settings;
  // hand-editing prefs.md also works).
  k8s_namespace?: string;
  k8s_context?: string;
  k8s_job_selector?: string;
  // D5b (A2-4): status.json directory — CPFS local mount read first;
  // kubectl exec cat is the fallback. "" = the batch bridge is off.
  k8s_batch_dir?: string;
}

// P1-1: Known sudo-provider models (Hermes custom_providers). Hardcoded for
// MVP — no IPC model-discovery. Free-form values still round-trip via the
// datalist combobox; these only seed the picker suggestions.
export const SUDO_PROVIDER = "sudo";
export const SUDO_MODELS = [
  "sudo/t9s/glm-5.2",
  "sudo/t9s/kimi-k3",
  "sudo/t9s/gpt-5",
  "sudo/t9s/gpt-5.6-sol",
  "sudo/t9s/deepseek-v4-flash",
  "sudo/t9s/claude-sonnet-4",
  "sudo/t9s/kimi-k2.7-code",
] as const;

export interface GetSettingsResponse {
  ok: boolean;
  error?: string;
  settings?: Settings;
}

export interface UpdateSettingsResponse {
  ok: boolean;
  error?: string;
}

// Type alias (not interface) so it is assignable to Tauri's InvokeArgs.
export type UpdateSettingsRequest = {
  settings: Partial<Settings>;
};

// ---------- M3: wiki browser + visibility ----------

// One distilled wiki note, listed by the daemon's `list_wiki` command.
export interface WikiNoteInfo {
  path: string;
  name: string;
  epoch: number;
  modified_at: string;
}

export interface ListWikiResponse {
  ok: boolean;
  error?: string;
  wiki_notes?: WikiNoteInfo[];
}

export interface ReadWikiResponse {
  ok: boolean;
  error?: string;
  wiki_content?: string;
}

// M12 (D-auto): one scheduled (or blocked) auto-distill as reported by the
// daemon's pending_counts. eta_unix drives the composer countdown chip; a
// blocked_reason entry has eta 0 and means the fold is paused for honesty
// reasons (the window outgrew the distill prompt budget) until a manual
// distill lands.
export interface AutoDistillCountdown {
  conversation_id: number;
  eta_unix: number;
  trigger: string;
  blocked_reason?: string;
}

// Daemon `pending_counts` (spec §3c fallback): per-workstream pending-diff
// counts plus the workstreams with a live run. JSON object keys arrive as
// strings; the caller converts to number keys. M12: also the auto-distill
// countdowns and in-flight distill state — the GUI discloses daemon
// triggers, it never owns them.
export interface PendingCountsResponse {
  ok: boolean;
  error?: string;
  pending_counts?: Record<string, number>;
  running_workstreams?: number[];
  // W6 (goal queue): per-workstream parked-goal queue depth, keyed like
  // pending_counts. The sidebar's parked pill is sourced from this —
  // the daemon's count is the authoritative depth.
  parked_goals?: Record<string, number>;
  auto_distill?: AutoDistillCountdown[];
  distilling?: boolean;
  distilling_convs?: number[];
  // 2026-08-26 memory-replay doctrine: unresolved heal_conflict rows
  // across the whole project — the Memory tab's banner count.
  stranded_memory_ops?: number;
  // Round-3 FIX F: the counted rows themselves, project-wide — the count
  // is project-wide, so the actionable rows must be too (a conflict on a
  // rotated lane would otherwise light the banner with zero rows to act
  // on). The Memory tab folds THIS list, not its own conversation's
  // events; resolve routes by each row's owning conversation.
  stranded_ops?: StrandedOpRow[];
  // Auto-distill daily-cap suspension disclosure: the Memory tab's
  // "今日额度已用完 · 预计恢复" chip. Absent while quiet, past the
  // horizon, or auto-distill disabled (FIX 3 — the chip never outlives
  // either).
  auto_distill_cap_resume?: AutoDistillCapResume | null;
}

// One daily-cap suspension as pending_counts discloses it (daemon
// AutoCapResumeInfo): resume_at_unix is the earliest quota release,
// rendered against the client clock; computed marks the pre-suspension-
// row journal fallback (oldest counted distill + 24h).
export interface AutoDistillCapResume {
  resume_at_unix: number;
  computed?: boolean;
}

// One OPEN heal_conflict row from pending_counts (wire shape). The
// identity fields are the resolve request's addressing halves:
// conversation_id is the receipt's OWNING conversation (routing and
// content-key half — a heal row may ride a rotated carrier lane).
export interface StrandedOpRow {
  conversation_id: number;
  layer: string;
  receipt_seq: number;
  detail?: string;
}

// Daemon `auto_distill_ctl` (M12): the chip's Cancel — disarms a scheduled
// (not yet fired) auto-distill. disarmed=false when nothing was armed.
export interface AutoDistillCtlResponse {
  ok: boolean;
  error?: string;
  disarmed?: boolean;
}

// ---------- M4: learning (memory.md / user.md proposals + apply) ----------

// The daemon tags every proposal with the file it targets.
export type MemoryTarget = "memory.md" | "user.md" | "skills";

// One learner-proposed rule from the memory_propose batch. evidence
// is optional — user-target proposals have no note evidence; projects is the
// daemon-verified recurrence set (display-only, never the LLM's own tags).
//
// M9: when target is "skills", rule holds the full composed SKILL.md content,
// name is the vetted kebab-case skill name.
//
// Panel-gated apply: reviews carries the panel verdicts for every gated
// proposal (all targets when the prefs `review:` panel is configured).
export interface MemoryProposal {
  target: MemoryTarget;
  rule: string;
  name?: string;    // M9: vetted skill name (skills target only)
  evidence?: string;
  contradicts?: string;
  projects?: string[];
  reviews?: ReviewResult[]; // panel verdicts (gated proposals)
}

// One proposal-batch reference (target + batch index) — the apply row's
// accepted/rejected lists use it; the GUI derives per-row outcomes.
export interface MemoryAcceptRef {
  target: MemoryTarget;
  index: number;
}

// One proposal batch (journal-only daemon storage: the memory_propose
// review_action whose epoch matches the latest distill) plus, once
// consumed, its decision (who applied and which refs landed).
export interface PendingMemoryBatch {
  epoch: number;
  seq: number;
  proposals: MemoryProposal[];
  reaffirm?: string[]; // daemon-internal, echoed for transparency
  consumed?: boolean;
  applyActor?: string; // "auto_panel" | human (absent on pre-panel rows)
  accepted?: MemoryAcceptRef[];
  rejected?: MemoryAcceptRef[];
}

// memory_proposals: the latest epoch's batch — epoch absent/0 = no batch
// at all. consumed=false is actionable (the human fallback); consumed=true
// is the outcome view (the panel-gated path consumed it, or a human did).
export interface MemoryProposalsResponse {
  ok: boolean;
  error?: string;
  epoch?: number;
  seq?: number;
  proposals?: MemoryProposal[];
  reaffirm?: string[];
  consumed?: boolean;
  apply_actor?: string;
  accepted?: MemoryAcceptRef[];
  rejected?: MemoryAcceptRef[];
}

// Type alias (not interface) so it is assignable to Tauri's InvokeArgs.
// `index` addresses a proposal by its position in the batch's proposals
// array (across both targets), exactly as the daemon validates it.
export type ApplyMemoryRequest = {
  conversationId: number;
  epoch: number;
  accepted: { target: MemoryTarget; index: number }[];
};

// apply_memory is all-or-nothing: ok:true + applied means every target was
// written and journaled; ok:false leaves the batch pending for retry.
export interface ApplyMemoryResponse {
  ok: boolean;
  error?: string;
  applied?: boolean;
}

// read_memory: the daemon constructs the three canonical paths itself;
// missing files come back as "".
export interface ReadMemoryResponse {
  ok: boolean;
  error?: string;
  memory_content?: string;
  archive_content?: string;
  user_content?: string;
}

// ---------- M5: curation (topic pages + index.md + pins) ----------

// curate: the curator rewrites wiki/topics/*.md + wiki/index.md from the
// full epoch-note set (generation-2 rule). wiki_path is "wiki/index.md";
// memory_proposals is 0 (the sidebar topic count comes from list_topics).
export interface CurateResponse {
  ok: boolean;
  error?: string;
  wiki_path?: string;
  memory_proposals?: number;
}

// pin: store a verbatim pin in .odo/pins.md. ok:false (e.g. the overflow
// refusal, which names the pin text) means nothing was written.
export interface PinResponse {
  ok: boolean;
  error?: string;
  applied?: boolean;
}

// One open heal_conflict (a stranded crash-recovery intent) surfaced for
// the Memory tab's review rows — the panel-facing projection of
// StrandedOpRow (round-3 FIX F: the daemon's project-wide pending_counts
// list is the ONLY row source; the per-conversation event fold rendered
// "N stranded" with zero actionable rows whenever the conflict rode a
// rotated lane). The daemon pairs conflicts with resolutions by
// (stranded_conversation, layer, receipt_seq) — the row identity and the
// resolve request both carry the conversation half.
export interface StrandedOp {
  layer: string;
  receiptSeq: number;
  // The receipt's owning conversation — the row's identity half, and the
  // routing key for resolve_heal_conflict (never the carrier).
  strandedConversation: number;
  detail?: string;
}

// resolve_heal_conflict: applied confirms the stranded body was restored
// (or the dismissal journaled); stranded_memory_ops is the post-action
// project-wide count so the badge converges without a second poll.
export interface ResolveHealConflictResponse {
  ok: boolean;
  error?: string;
  applied?: boolean;
  stranded_memory_ops?: number;
}

// read_pins: .odo/pins.md content, "" when absent (same shape as
// read_memory).
export interface ReadPinsResponse {
  ok: boolean;
  error?: string;
  memory_content?: string;
}

// list_topics: one WikiNoteInfo per wiki/topics/<slug>.md — Name is the
// parsed topic title (first `# ` line, falling back to the slug) and Epoch
// is always 0 (topics are not per-epoch notes).
export interface ListTopicsResponse {
  ok: boolean;
  error?: string;
  wiki_notes?: WikiNoteInfo[];
}

// ---------- M6: precision + ledger ----------

// Note: the ledger IPC query keeps its daemon command; the GUI reads
// ledger.md through read_file now (A3-2 Preview pathway), so the response
// type went with it.
//
// contradictions: the conversation's note-retraction events
// (memory_update{layer:"note", cause:"retract"}) for the wiki browser's
// retracted badges.
export interface ContradictionsResponse {
  ok: boolean;
  error?: string;
  events?: OdoEvent[];
}

// E P2: cross-conversation search
export interface SearchResult {
  event: OdoEvent;
  workstream_id: number;
  workstream_name: string;
  conversation_id: number;
}

export interface SearchEventsResponse {
  ok: boolean;
  error?: string;
  search_results?: SearchResult[];
}

// M8 (Skills): skill metadata and CRUD responses.
export interface SkillInfo {
  name: string;
  description: string;
  keywords?: string[];
  path: string;
  origin: string; // "human" | "ported" | "agent-authored"
  scope: string;  // "global" | "project"
}

export interface ListSkillsResponse {
  ok: boolean;
  error?: string;
  skills?: SkillInfo[];
}

export interface ReadSkillResponse {
  ok: boolean;
  error?: string;
  skill_content?: string;
}

export interface UpdateSkillResponse {
  ok: boolean;
  error?: string;
}

// ---------- M12 (D-todo): durable plan layer ----------

// One journaled todo item as it appears in a todo_merge snapshot, plus the
// read-time flags the daemon derives from fold markers (swept/stale are
// never journaled — the GUI recomputes them with the same arithmetic).
export interface TodoViewItem {
  id: string;                     // daemon-assigned t<N>
  text: string;
  status: "open" | "done" | "struck";
  origin_seq: number;
  updated_seq: number;
  stale: boolean;                 // open, untouched through ≥3 folds (~ marker)
  swept: boolean;                 // done/struck with a fold boundary past updated_seq
}

// The op set the GUI may emit via todo_update (reopen is user-only — an
// agent's fenced block never carries it).
export type TodoUpdateAction = "add" | "done" | "strike" | "reopen" | "reword";

// Daemon `todo_update`: ok with the journaled todo_merge event; semantic
// rejects (unknown id, open cap, …) arrive INSIDE that event's payload as
// ops_rejected, not as an IPC error.
export interface TodoUpdateResponse {
  ok: boolean;
  error?: string;
  event?: OdoEvent;
}

// ---------- P2 (OMP stats): provider usage + grievances ----------

// One usage limit entry from `omp usage --json`. The daemon passes the
// raw omp JSON through; these interfaces describe the shape the GUI
// renders. Unknown/extra fields are ignored (omp may add new ones).
export interface OmpUsageWindow {
  id: string;
  label: string;
  durationMs: number;
  resetsAt: number;
}

export interface OmpUsageAmount {
  used: number;
  limit: number;
  remaining: number;
  usedFraction: number;
  remainingFraction: number;
  unit: string;
}

export interface OmpUsageLimit {
  id: string;
  label: string;
  window: OmpUsageWindow;
  amount: OmpUsageAmount;
  status: string;
}

export interface OmpUsageReport {
  provider: string;
  fetchedAt: number;
  limits: OmpUsageLimit[];
}

export interface OmpUsageData {
  generatedAt?: number;
  reports?: OmpUsageReport[];
}

// Grievances: omp grievances --json returns an array of issue objects.
// The exact shape varies; the GUI only needs the count and optionally
// the first few for display. We type it as unknown[] for safety.
export type OmpGrievance = Record<string, unknown>;

// Merged response from the daemon's omp_usage handler:
//   { usage: <omp usage JSON>, grievances: <array>,
//     errors?: string[] }
// Fields are omitempty on the Go side — absent (undefined) when the
// corresponding omp command failed, present when it succeeded.
export interface OmpUsageMerged {
  usage?: OmpUsageData | null;
  grievances?: OmpGrievance[] | null;
  errors?: string[];
}

export interface OmpUsageResponse {
  ok: boolean;
  error?: string;
  omp_usage?: OmpUsageMerged;
}

// ---------- UX-2 (D5 Stage 0 / A2-1): k8s_status wire types ----------
// The daemon passes kubectl `get jobs,pods -o json` items through raw
// (swap-friendly). The GUI reads only the fields its chip rows render;
// unknown/extra kubectl fields are ignored (OmpUsage posture).

export interface K8sRuntimeObjectMeta {
  name?: string;
  // A4: kubectl items self-identify — the daemon flat-merges across
  // namespaces and the GUI groups by this field; absent on legacy
  // single-ns payloads (the ns is implied by configured-ness then).
  namespace?: string;
  creationTimestamp?: string;
}

export interface K8sJobCondition {
  type?: string; // Complete | FailureTarget | SuccessCriteriaMet | Suspended
  status?: string; // "True" | "False" | "Unknown"
  reason?: string;
  message?: string;
}

export interface K8sJob {
  metadata?: K8sRuntimeObjectMeta;
  spec?: {
    completions?: number;
    parallelism?: number;
  };
  status?: {
    active?: number;
    succeeded?: number;
    failed?: number;
    startTime?: string;
    completionTime?: string;
    conditions?: K8sJobCondition[];
  };
}

export interface K8sPod {
  metadata?: K8sRuntimeObjectMeta;
  status?: { phase?: string };
}

// Degradation contract (A2-1, verbatim): data may be absent, the reason
// may never be absent. off = feature disabled (no chip, no tab, no
// polling); every other class is a VISIBLE dimmed chip with the reason
// in its popover. bad_namespace is rejected daemon-side before any exec —
// it covers BOTH a rejected namespace element and an over-cap list (N > 5);
// detail names the offender(s) or the cap.
export type K8sUnavailableReason =
  | "off"
  | "ENOENT"
  | "timeout"
  | "auth"
  | "unreachable"
  | "bad_namespace";

// A4 D3: one CONFIGURED namespace's outcome row, in configured order. A
// failed namespace degrades HERE (ok:false + reason + the daemon's capped
// detail tail) — partial availability is a healthy chip with degraded
// rows, NEVER a third chip state.
export interface K8sNsStatusRow {
  name: string;
  ok: boolean;
  reason?: K8sUnavailableReason;
  detail?: string;
  job_count?: number;
}

export interface K8sStatus {
  available: boolean;
  reason?: K8sUnavailableReason;
  // kubectl's stderr tail behind a non-off reason — the daemon caps it at
  // 1024 bytes AT CAPTURE (LimitReader pipe). The popover renders it dimmed
  // below the canned reasonLabel sentence (display-capped ~240 via
  // capTransportErr). Absent pre-exec (off/bad_namespace/ENOENT exec
  // nothing, so there is no subprocess output to carry).
  detail?: string;
  // FLAT-MERGED across the answering namespaces — group rows by
  // metadata.namespace; no per-ns section headers anywhere.
  jobs?: K8sJob[];
  pods?: K8sPod[];
  truncated?: boolean;
  // One row per CONFIGURED namespace in configured order — present
  // whenever the pref parses non-empty, including total failure. Absent
  // only on pre-A4 daemon replies (treated as single legacy ns).
  namespaces?: K8sNsStatusRow[];
  fetched_unix?: number;
}

// ---------- D5b (A2-4): k8s_batch_status wire types ----------
// status.json rows (schema pinned in docs/design/d5b-batch-status.md):
// local CPFS mount read first, kubectl exec cat fallback. The daemon
// computes stale (>90s heartbeat) and ships the raw stamp beside it.
export interface K8sBatch {
  batch: string;
  stage?: string;
  total?: number;
  done?: number;
  errs?: number;
  rate_per_min?: number;
  updated_unix?: number;
  status?: "running" | "done" | "failed" | string;
  stale?: boolean;
  // Per-row degradation: schema_mismatch / unparseable / unreadable /
  // pod_not_found / ambiguous_pod / no_pod_selector — the row stays
  // VISIBLE with its cause, never silently dropped.
  reason?: string;
}

export interface K8sBatchStatus {
  available: boolean;
  // Whole-response classes only: "off" (k8s_batch_dir unset), "ENOENT"
  // (kubectl missing for the fallback), "local_missing" (dir unreadable
  // and no k8s fallback configured).
  reason?: string;
  detail?: string;
  batches?: K8sBatch[];
  truncated?: boolean;
}

export interface K8sBatchStatusResponse {
  ok: boolean;
  error?: string;
  k8s_batch_status?: K8sBatchStatus;
}

export interface K8sStatusResponse {
  ok: boolean;
  error?: string;
  k8s_status?: K8sStatus;
}
